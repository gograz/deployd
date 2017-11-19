package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
)

const (
	SAVE_CMD = iota
	GET_CMD  = iota
)

type controller struct {
	ctx        context.Context
	wg         *sync.WaitGroup
	logger     *logrus.Logger
	errors     chan error
	cancelFunc func()
}

type githubPushEventData struct {
	Ref string `json:"ref"`
}

func checkProjectFolder(folder string) error {
	makefile := path.Join(folder, "Makefile")
	_, err := os.Stat(makefile)
	return err
}

type lockerCommand struct {
	Command      int
	Status       string
	Output       string
	ResponseChan chan lockerCommand
}

func loadStatusFromFile(filepath string) (string, string, error) {
	raw, err := ioutil.ReadFile(filepath)
	if err != nil {
		return "", "", err
	}
	data := string(raw)
	elements := strings.SplitN(data, "\n", 2)
	return elements[0], elements[1], nil
}

func saveStatusToFile(status, output, filepath string) error {
	return ioutil.WriteFile(filepath, []byte(fmt.Sprintf("%s\n%s", status, output)), 0600)
}

func (c *controller) startStatusLocker(cmdChan chan lockerCommand, statusFile string) {
	defer c.logger.Info("Stopping status locker")
	defer c.wg.Done()
	lastStatus := "not started"
	lastOutput := "not started"

	for {
		select {
		case <-c.ctx.Done():
			return
		case cmd := <-cmdChan:
			if cmd.Command == SAVE_CMD {
				lastStatus = cmd.Status
				lastOutput = cmd.Output
				if err := saveStatusToFile(lastStatus, lastOutput, statusFile); err != nil {
					c.logger.Printf("Failed to write to status file: %s\n", err.Error())
				}
			} else if cmd.Command == GET_CMD {
				cmd.Status = lastStatus
				cmd.Output = lastOutput
				cmd.ResponseChan <- cmd
			}
		case <-time.After(time.Second * 1):
		}
	}

}

func (c *controller) startWorker(projectFolder string, workChan chan struct{}, lockerChan chan lockerCommand) {
	defer c.logger.Info("Stopping worker")
	defer c.wg.Done()
	c.logger.Printf("Starting worker for %s\n", projectFolder)
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-workChan:
			c.logger.Println("Got a job to do")
			lockerChan <- lockerCommand{Command: SAVE_CMD, Status: "started", Output: ""}
			cmd := exec.Cmd{Dir: projectFolder, Path: "/usr/bin/make", Args: []string{"deploy"}}
			output, err := cmd.CombinedOutput()
			status := "ok"
			if err != nil {
				status = "failed"
				c.logger.Println("Job failed")
			} else {
				c.logger.Println("Job completed")
			}
			lockerChan <- lockerCommand{Command: SAVE_CMD, Output: string(output), Status: status}
		case <-time.After(time.Second * 1):
		}
	}
}

func (c *controller) startHTTPD(secret, host, branch string, workChan chan struct{}, lockerChan chan lockerCommand) {
	c.logger.Printf("Starting HTTPD on %s\n", host)
	defer c.wg.Done()
	defer c.logger.Info("Stopping HTTPD")
	mux := http.NewServeMux()
	srv := http.Server{
		Handler: mux,
	}
	mux.HandleFunc("/", func(rw http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			respChan := make(chan lockerCommand, 1)
			lockerChan <- lockerCommand{Command: GET_CMD, ResponseChan: respChan}
			status := <-respChan
			if status.Status == "failed" {
				http.Error(rw, "Last deployement failed", http.StatusInternalServerError)
			} else {
				fmt.Fprint(rw, "Last deployment succeeded")
			}

		} else {
			payload, err := ioutil.ReadAll(r.Body)
			if err != nil {
				http.Error(rw, "Failed to read the request body", http.StatusInternalServerError)
				return
			}
			if err = verifySignature(&payload, r.Header.Get("X-Hub-Signature"), secret); err != nil {
				http.Error(rw, fmt.Sprintf("Invalid signature: %s", err.Error()), http.StatusBadRequest)
				return
			}
			if branch != "" {
				eventData := githubPushEventData{}
				if err = json.Unmarshal(payload, &eventData); err != nil {
					c.logger.Printf("Failed to decode body: %s", err.Error())
					http.Error(rw, fmt.Sprintf("Failed to decode body"), http.StatusBadRequest)
					return
				}
				if eventData.Ref != "refs/heads/"+branch {
					http.Error(rw, "Not-configured branch detected. No operation required.", http.StatusOK)
					return
				}
			}
			select {
			case workChan <- struct{}{}:
				fmt.Fprintf(rw, "Deployment started")
				return
			default:
				http.Error(rw, fmt.Sprintf("Deployement already in progress"), http.StatusConflict)
				return
			}
		}
	})
	go func() {
		timeoutCtx, cancel := context.WithTimeout(context.Background(), time.Second*5)
		defer cancel()
		<-c.ctx.Done()
		srv.Shutdown(timeoutCtx)
	}()
	srv.Addr = host
	if err := srv.ListenAndServe(); err != nil {
		if err != http.ErrServerClosed {
			c.errors <- err
		}
	}
}

func main() {
	log := logrus.New()
	var projectFolder string
	var host string
	var secret string
	var statusFile string
	var branch string
	var verbose bool

	pflag.StringVar(&projectFolder, "project", "", "Project folder containing the Makefile")
	pflag.StringVar(&host, "host", "127.0.0.1:9876", "Interface and port to listen on")
	pflag.StringVar(&secret, "secret", "", "Github webhook secret")
	pflag.StringVar(&statusFile, "status-file", "", "Status file")
	pflag.StringVar(&branch, "branch", "", "Restrict deployd to only trigger on a specific branch change")
	pflag.BoolVar(&verbose, "verbose", false, "Verbose logging")
	pflag.Parse()

	if verbose {
		log.SetLevel(logrus.DebugLevel)
	} else {
		log.SetLevel(logrus.WarnLevel)
	}

	if secret == "" {
		log.Fatalln("You have to specify a secret using --secret")
	}
	if projectFolder == "" {
		log.Fatalln("You have to specify a project folder using --project")
	}
	if statusFile == "" {
		log.Fatalln("You have to specify a status file using --status-file")
	}
	if err := checkProjectFolder(projectFolder); err != nil {
		log.Fatalf("The project folder appears to be invalid: %s\n", err.Error())
	}

	previousStatus, previousOutput, err := loadStatusFromFile(statusFile)
	if err != nil && !os.IsNotExist(err) {
		log.WithError(err).Fatal("Failed to load status file")
	}

	wg := sync.WaitGroup{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ctrl := controller{
		ctx:        ctx,
		cancelFunc: cancel,
		wg:         &wg,
		logger:     log,
		errors:     make(chan error, 3),
	}

	sigChan := make(chan os.Signal)
	go func() {
		sig := <-sigChan
		log.Warnf("Signal received: %s", sig)
		cancel()
	}()
	signal.Notify(sigChan, syscall.SIGINT)

	workChannel := make(chan struct{}, 1)
	lockerChan := make(chan lockerCommand, 5)
	lockerChan <- lockerCommand{Command: SAVE_CMD, Status: previousStatus, Output: previousOutput}

	wg.Add(3)

	go ctrl.startStatusLocker(lockerChan, statusFile)
	go ctrl.startWorker(projectFolder, workChannel, lockerChan)
	go ctrl.startHTTPD(secret, host, branch, workChannel, lockerChan)

	wg.Wait()

	select {
	case e := <-ctrl.errors:
		log.WithError(e).Fatal("An error occured")
	default:
	}
}

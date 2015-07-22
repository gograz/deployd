package main

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"
)

const (
	SAVE_CMD = iota
	GET_CMD  = iota
)

type GithubPushEventData struct {
	Ref string `json:"ref"`
}

func checkProjectFolder(folder string) error {
	makefile := path.Join(folder, "Makefile")
	_, err := os.Stat(makefile)
	return err
}

type LockerCommand struct {
	Command      int
	Status       string
	Output       string
	ResponseChan chan LockerCommand
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

func startStatusLocker(cmdChan chan LockerCommand, statusFile string) {
	lastStatus := "not started"
	lastOutput := "not started"

	for {
		select {
		case cmd := <-cmdChan:
			if cmd.Command == SAVE_CMD {
				lastStatus = cmd.Status
				lastOutput = cmd.Output
				if err := saveStatusToFile(lastStatus, lastOutput, statusFile); err != nil {
					log.Printf("Failed to write to status file: %s\n", err.Error())
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

func startWorker(projectFolder string, workChan chan struct{}, lockerChan chan LockerCommand) {
	log.Printf("Starting worker for %s\n", projectFolder)
	for {
		select {
		case <-workChan:
			log.Println("Got a job to do")
			lockerChan <- LockerCommand{Command: SAVE_CMD, Status: "started", Output: ""}
			cmd := exec.Cmd{Dir: projectFolder, Path: "/usr/bin/make", Args: []string{"deploy"}}
			output, err := cmd.CombinedOutput()
			status := "ok"
			if err != nil {
				status = "failed"
				log.Println("Job failed")
			} else {
				log.Println("Job completed")
			}
			lockerChan <- LockerCommand{Command: SAVE_CMD, Output: string(output), Status: status}
		case <-time.After(time.Second * 1):
		}
	}
}

type SignatureValidationError struct {
	Expected string
	Actual   string
}

func (e SignatureValidationError) Error() string {
	return fmt.Sprintf("Signature validation failed. Expected: %s, actual: %s", e.Expected, e.Actual)
}

func verifySignature(payload *[]byte, signature, secret string) error {
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write(*payload)
	checkSum := mac.Sum(nil)
	expectedSignature := fmt.Sprintf("sha1=%x", checkSum)
	if expectedSignature != signature {
		return SignatureValidationError{Expected: expectedSignature, Actual: signature}
	}
	return nil
}

func startHTTPD(secret, host, branch string, workChan chan struct{}, lockerChan chan LockerCommand) {
	log.Printf("Starting HTTPD on %s\n", host)
	http.HandleFunc("/", func(rw http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			respChan := make(chan LockerCommand, 1)
			lockerChan <- LockerCommand{Command: GET_CMD, ResponseChan: respChan}
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
				eventData := GithubPushEventData{}
				if err = json.Unmarshal(payload, &eventData); err != nil {
					log.Printf("Failed to decode body: %s", err.Error())
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
	http.ListenAndServe(host, nil)
}

func main() {
	projectFolder := flag.String("project", "", "Project folder containing the Makefile")
	host := flag.String("host", "127.0.0.1:9876", "Interface and port to listen on")
	secret := flag.String("secret", "", "Github webhook secret")
	statusFile := flag.String("statusFile", "", "Status file")
	branch := flag.String("branch", "", "Restrict deployd to only trigger on a specific branch change")
	flag.Parse()

	if *secret == "" {
		log.Fatalln("You have to specify a secret using -secret")
	}
	if *projectFolder == "" {
		log.Fatalln("You have to specify a project folder using -project")
	}
	if *statusFile == "" {
		log.Fatalln("You have to specify a status file using -statusFile")
	}
	if err := checkProjectFolder(*projectFolder); err != nil {
		log.Fatalf("The project folder appears to be invalid: %s\n", err.Error())
	}

	workChannel := make(chan struct{}, 1)
	lockerChan := make(chan LockerCommand, 5)
	previousStatus, previousOutput, err := loadStatusFromFile(*statusFile)
	if err == nil {
		lockerChan <- LockerCommand{Command: SAVE_CMD, Status: previousStatus, Output: previousOutput}
	}
	go startStatusLocker(lockerChan, *statusFile)
	go startWorker(*projectFolder, workChannel, lockerChan)
	startHTTPD(*secret, *host, *branch, workChannel, lockerChan)
}

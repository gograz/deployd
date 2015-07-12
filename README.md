# DeployD

This is a little helper daemon (hence the D) for the deployments on
GoGraz.org. The idea is pretty simple: Once we push something to the master
branch of our site's Github repository we want the site to be updated. A webhook
on Github is triggered that notifies DeployD which in turn goes into a
directory and executes `make deploy` there.

## Usage

So how do you run this? First you should prepare a folder that contains a
Makefile with a `deploy` target. What that does is completely up to your
site. In our case `make deploy` generates some CSS out of SCSS files and runs
hugo for the production environment.

Once you have that, simply start deployd like this:

```
$ deployd -project /path/to/your/project -host 127.0.0.1:8000 -secret yourSecretToken -statusFile /path/to/statusfile
```

DeployD listens on an interface and port that you specify with the `-host` flag
and starts a simple HTTP server which you can then put behind an nginx or what
have you.

Now you go to your site's Github settings page and set the webhook endpoint to
wherever you've exposed the service to as well as enter the secret you specified
when starting it.

DeployD will listen to any requests that is thrown its way only distinguishing
between GET and POST requests to either return the latest deployment status or
triggering a new deployment.

Internally, DeployD only allowed one update of the system to run in
parallel. Any other request that is made while one update is running is replied
to with a `409 Conflict` status.


## How to build

In order to build DeployD locally you will need gb. You can find more details
about that on [getgb.io](http://getgb.io/).


## Current limitations

* If a deployment fails you only find out by either accessing DeployD via HTTP
  GET or looking at the content of the status file. In the future it might also
  send an email to a configured e-mail address.
* `make` has to be installed in `/usr/bin/`.

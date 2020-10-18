package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// StringArray array of string
type StringArray []string

// ArrayOps array's operation
type ArrayOps interface {
	IndexOf(item interface{})
}

// NeutronResponse represent neutron command's response
type NeutronResponse struct {
	ID                 string `json:"id"`
	ProvisioningStatus string `json:"provisioning_status"`
}

// CommandResult saved command analytics data.
type CommandResult struct {
	Seq             int                    `json:"seqnum"`
	Command         string                 `json:"command"`
	Out             map[string]interface{} `json:"output"`
	Err             string                 `json:"error"`
	ExitCode        int                    `json:"exitcode"`
	Duration        time.Duration          `json:"duration"`
	Checked         string                 `json:"success"`
	CheckedDuration time.Duration          `json:"done_duration"`
}

var (
	logger  = log.New(os.Stdout, "", log.LstdFlags)
	usage   = fmt.Sprintf("Usage: \n\n    %s [command arguments] -- <neutron command and arguments>[ ++ variable-definition]\n\n", os.Args[0])
	example = fmt.Sprintf("Example:\n\n    %s --concurrency --output /dev/stdout \\\n    "+
		"-- neutron lbaas-loadbalancer-create --name lb%s %s \\\n    ++ x:1-5 y:private-subnet,public-subnet\n\n", os.Args[0], "{x}", "{y}")
	varRegexp = regexp.MustCompile(`%\{[a-zA-Z_][a-zA-Z0-9_]*\}`)
	cmdList   = []string{}

	concurrency int
	output      string

	cmdResults = []CommandResult{}
	cmdPrefix  = "neutron lbaas-"
)

func main() {

	HandleArguments()

	if !strings.Contains(strings.Join(os.Environ(), ","), "OS_USERNAME=") {
		fmt.Println("No OS_USERNAME environment found. Execute `source <path/to/openrc>` first!")
		os.Exit(1)
	}

	neutron, err := exec.LookPath("neutron")
	if err != nil {
		logger.Fatal(err)
	}
	logger.Printf("neutron command: %s\n", neutron)

	RunCmds()

	jd, _ := json.MarshalIndent(cmdResults, "", "  ")
	fmt.Printf("%s\n", jd)
}

// RunCmds Execute the generated commands analyze result.
func RunCmds() {
	for i, n := range cmdList {
		fullCmd := fmt.Sprintf("%s%s", cmdPrefix, n)
		logger.Printf("Command(%d/%d): '%s' starts\n", i+1, len(cmdList), fullCmd)

		cr := RunCommand(fullCmd)

		logger.Printf("Command '%s' exits with: %d, executing time: %s \n", cr.Command, cr.ExitCode, cr.Duration)
		cmdResults = append(cmdResults, cr)

		// check the command execution.
		if cr.ExitCode != 0 {
			continue
		}
		logger.Printf("Checking Execution: \n")
		CheckExecution(cr)
	}
}

// CheckExecution check the execution in backend is done.
func CheckExecution(rlt CommandResult) {
	args := strings.Split(rlt.Command, " ")
	subs := strings.Split(args[1], "-")
	resourceType, operation := subs[1], subs[2]

	fs := time.Now()
	if operation == "create" || operation == "update" {
		checkCmd := fmt.Sprintf("neutron lbaas-%s-show %s", resourceType, rlt.Out["id"])
		logger.Printf("Checking Command: %s\n", checkCmd)

		for true {
			cr := RunCommand(checkCmd)
			if cr.ExitCode != 0 {
				rlt.Checked = fmt.Sprintf("Failed to check execution of %s: %s", rlt.Command, cr.Err)
				break
			}

			var stat NeutronResponse
			b, _ := json.Marshal(cr.Out)
			_ = json.Unmarshal(b, &stat)
			if strings.HasPrefix(stat.ProvisioningStatus, "PENDING_") {
				continue
			} else {
				rlt.Checked = fmt.Sprintf("%s: %s", stat.ID, stat.ProvisioningStatus)
				break
			}
		}
	} else { // 'show' 'list' 'delete' no need to check
		rlt.Checked = fmt.Sprintf("%s done", args[1])
	}
	fe := time.Now()

	rlt.CheckedDuration = fe.Sub(fs) + rlt.Duration
}

// RunCommand run the command and fill CommandResult body
func RunCommand(cmd string) CommandResult {
	cmdArgs := strings.Split(cmd, " ")
	cmdArgs = append(cmdArgs, "--format", "json")
	var out, err bytes.Buffer
	c := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	c.Env = os.Environ()
	c.Stdout = &out
	c.Stderr = &err

	cr := CommandResult{
		Seq:     0,
		Command: cmd,
	}

	fs := time.Now()
	e := c.Start()
	if e != nil {
		err.WriteString(e.Error())
	} else {
		e = c.Wait()
		if e != nil {
			err.WriteString(e.Error())
		} else {
			var o map[string]interface{}
			e = json.Unmarshal(out.Bytes(), &o)
			if e != nil {
				cr.Out = map[string]interface{}{
					"message": out.Bytes(),
				}
			} else {
				cr.Out = o
			}
		}
	}
	cr.Err = err.String()
	fe := time.Now()
	cr.ExitCode = c.ProcessState.ExitCode()
	cr.Duration = fe.Sub(fs)

	return cr
}

// HandleArguments handle user's input.
func HandleArguments() {
	flag.IntVar(&concurrency, "concurrency", 1, "If or not do the operations concurrently.")
	flag.StringVar(&output, "output", "/dev/stdout", "output the result")

	flag.Usage = PrintUsage
	flag.Parse()

	logger.Printf("concurrency number: %v, output: %s\n", concurrency, output)

	neutronArgsIndex := StringArray(os.Args).IndexOf("--")
	if neutronArgsIndex == -1 {
		logger.Fatal(usage)
	}

	variableArgsIndex := StringArray(os.Args).IndexOf("++")
	if variableArgsIndex == -1 {
		variableArgsIndex = len(os.Args)
	}

	neutronCmdArgs := strings.Join(os.Args[neutronArgsIndex+1:variableArgsIndex], " ")
	logger.Printf("Command template: %s\n", neutronCmdArgs)

	variables := map[string]StringArray{}

	varStart := false

	for _, n := range os.Args[neutronArgsIndex+1:] {
		if n == "++" {
			varStart = true
			continue
		}

		if !varStart {
			matches := varRegexp.FindAllString(n, -1)
			for _, m := range matches {
				logger.Printf("matched: %s\n", m)
				l := len(m)
				varName := m[2 : l-1]
				variables[varName] = []string{}
			}
		} else {
			for k := range variables {
				if strings.HasPrefix(n, fmt.Sprintf("%s:", k)) {
					kvp := strings.Split(n, ":")
					v := ParseVarValues(strings.Join(kvp[1:], ":"))
					variables[k] = append(variables[k], v...)
				}
			}
		}
	}

	for k, v := range variables {
		logger.Printf("%15s: %v\n", k, v)
	}

	ConstructFromTemplate(neutronCmdArgs, variables)
}

// PrintUsage print the usage
func PrintUsage() {
	fmt.Fprintf(os.Stderr, usage)
	fmt.Fprintf(os.Stderr, example)
	fmt.Fprintf(os.Stderr, "Command Arguments: \n\n")
	flag.PrintDefaults()
	fmt.Fprintf(os.Stderr, "\n")
}

// ConstructFromTemplate recursively generate the command from templete
func ConstructFromTemplate(template string, variables map[string]StringArray) {
	varInTmp := varRegexp.FindString(template)
	if varInTmp == "" {
		cmdList = append(cmdList, template)
		return
	}
	l := len(varInTmp)
	varName := varInTmp[2 : l-1]

	r := regexp.MustCompile(varInTmp)

	for _, k := range variables[varName] {
		replaced := r.ReplaceAllString(template, k)
		ConstructFromTemplate(replaced, variables)
	}
}

// ParseVarValues parse the value ranges to actual value list
// Supports: '-' num list and ',' list
//		1-5
// 		a,b,c
// 		1-3,4,6-9,a,b,c
func ParseVarValues(v string) []string {
	rlt := []string{}
	ls := strings.Split(v, ",")
	p := regexp.MustCompile(`^\d+\-\d+$`)
	for _, n := range ls {
		matched := p.MatchString(n)
		if matched {
			se := strings.Split(n, "-")
			s, _ := strconv.Atoi(se[0])
			e, _ := strconv.Atoi(se[1])
			for i := s; i <= e; i++ {
				rlt = append(rlt, fmt.Sprintf("%d", i))
			}
		} else {
			rlt = append(rlt, n)
		}
	}
	return rlt
}

// IndexOf Implement the StringArray's IndexOf
func (sa StringArray) IndexOf(item string) int {
	for i, n := range sa {
		if n == item {
			return i
		}
	}
	return -1
}

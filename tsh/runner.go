package tsh

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/adzimzf/tpot/config"
)

type TSH struct {
	proxy              *config.Proxy
	userLogin, dstHost string

	// abstract the exec.Command
	cmdExec func(name string, arg ...string) CmdExecutor

	minVersion Version
}

type CmdExecutor interface {
	Run() (cmdResult, error)
}

type cmdResult struct {
	stdOut, stdErr *bytes.Buffer
}

type cmdType struct {
	*exec.Cmd
}

func Command(name string, arg ...string) CmdExecutor {
	return &cmdType{exec.Command(name, arg...)}
}

func (c *cmdType) Run() (cmdResult, error) {
	var stdOut, stdErr = &bytes.Buffer{}, &bytes.Buffer{}
	c.Cmd.Stdout = stdOut
	c.Cmd.Stderr = stdErr
	err := c.Cmd.Run()
	return cmdResult{stdOut, stdErr}, err
}

// tshBinary is the `tsh` binary where we depends
const tshBinary = "tsh"

// tshVersion is the supported tsh binary Version
const tshVersion = "v4.1.11"

// ErrUnsupportedVersion indicates the current tsh version is not supported
var ErrUnsupportedVersion = fmt.Errorf("unsupported version")

// SSH run the `tsh ssh` commands
func (t *TSH) SSH(username, host string) error {
	args, err := t.getProxyFlags()
	if err != nil {
		return err
	}

	args = append(args, t.authFlags()...)

	ipAddress, ok := t.proxy.Node.LookUpIPAddress(host)
	if !ok {
		return fmt.Errorf("couldn't find IP address")
	}

	args = append(args, "-l", username, ipAddress)

	cmd := exec.Command(t.tshBinary(), append([]string{"ssh"}, args...)...)
	cmd.Stdout = os.Stdout
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// ListNodes get the list nodes from proxy
func (t *TSH) ListNodes() (config.Node, error) {

	if err := t.Login(); err != nil {
		return config.Node{}, err
	}

	args, err := t.getProxyFlags()
	if err != nil {
		return config.Node{}, err
	}

	cmd := exec.Command(t.tshBinary(), append([]string{"ls"}, args...)...)
	var stdOut, stdErr = &bytes.Buffer{}, &bytes.Buffer{}
	cmd.Stdout = stdOut
	cmd.Stdin = os.Stdin
	cmd.Stderr = stdErr
	if err = cmd.Run(); err != nil {
		return config.Node{}, err
	}
	if errStr := stdErr.String(); errStr != "" {
		return config.Node{}, errors.New(errStr)
	}

	return parseNodesFromString(stdOut.String()), nil
}

// Version return the short tsh Version
//
// the tsh Version formatting is like this
// Teleport v2.4.5.1 git:v2.4.5-19-g4901c48-dirty
// it'll only return the v2.4.5.1
func (t *TSH) Version() (*Version, error) {
	cmd := exec.Command(t.tshBinary(), "version")
	var stdOut, stdErr = &bytes.Buffer{}, &bytes.Buffer{}
	cmd.Stdout = stdOut
	cmd.Stdin = os.Stdin
	cmd.Stderr = stdErr
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	if errStr := stdErr.String(); errStr != "" {
		return nil, errors.New(errStr)
	}
	out := stdOut.String()
	if out == "" {
		return nil, fmt.Errorf("std out is empty")
	}

	return NewVersion(out)
}

// Status return the tsh proxy status
// this method is supported since tsh Version v2.6.1
func (t *TSH) Status() (*config.ProxyStatus, error) {
	cv, err := t.Version()
	if err != nil {
		return nil, err
	}
	if !t.minVersion.IsSupported(cv) {
		return nil, ErrUnsupportedVersion
	}

	proxyFlags, err := t.getProxyFlags()
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(t.tshBinary(), append([]string{"status"}, proxyFlags...)...)
	var stdOut, stdErr = &bytes.Buffer{}, &bytes.Buffer{}
	cmd.Stdout = stdOut
	cmd.Stdin = os.Stdin
	cmd.Stderr = stdErr
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	if errStr := stdErr.String(); errStr != "" {
		return nil, errors.New(errStr)
	}
	out := stdOut.String()
	if out == "" {
		return nil, fmt.Errorf("std out is empty")
	}

	return t.parseStringToStatus(out), err
}

func (t *TSH) parseStringToStatus(str string) *config.ProxyStatus {
	str = strings.Replace(str, ">", "", -1)
	lines := strings.Split(str, "\n")
	res := &config.ProxyStatus{}
	for _, line := range lines {
		kv := strings.Split(line, ":")
		if len(kv) <= 1 {
			continue
		}
		switch strings.TrimSpace(kv[0]) {
		case "Logged in as":
			res.LoginAs = strings.TrimSpace(kv[1])
		case "Roles":
			res.Roles = trimSliceString(strings.Split(strings.TrimSpace(kv[1]), ","))
		case "Logins":
			res.UserLogins = trimSliceString(strings.Split(strings.TrimSpace(kv[1]), ","))
		}
	}
	return res
}

func trimSliceString(list []string) (res []string) {
	for _, s := range list {
		res = append(res, strings.TrimSpace(s))
	}
	return
}

func (t *TSH) Login() error {

	if t.isLogin() {
		return nil
	}

	args, err := t.getProxyFlags()
	if err != nil {
		return err
	}

	args = append(args, t.authFlags()...)

	cmd := exec.Command(t.tshBinary(), append([]string{"login"}, args...)...)
	cmd.Stdout = os.Stdout
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stdin
	return cmd.Run()
}

type Profile struct {
	URL        string
	ValidUntil time.Time
}

// isLogin return true if the user is already login
func (t *TSH) isLogin() bool {
	cmd := t.cmdExec(t.tshBinary(), "status")
	res, err := cmd.Run()
	if err != nil {
		return false
	}

	if res.stdErr.String() != "" {
		return false
	}

	targetProfile := t.proxy.Address
	scanner := bufio.NewScanner(strings.NewReader(res.stdOut.String()))
	var currentProfile *Profile
	profileMap := make(map[string]*Profile)

	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "Profile URL:") {
			if currentProfile != nil && currentProfile.URL != "" {
				profileMap[currentProfile.URL] = currentProfile
			}
			line = strings.Replace(line, ">", "", -1)
			line = strings.Replace(line, "Profile URL:", "", -1)
			currentProfile = &Profile{
				URL: strings.TrimSpace(line),
			}
		} else if strings.Contains(line, "Valid until:") {
			line = strings.TrimSpace(line)
			timeString := strings.Split(strings.TrimPrefix(line, "Valid until:"), "[")[0]
			timeString = strings.TrimSpace(timeString)
			layout := "2006-01-02 15:04:05 -0700 WIB"
			validUntil, _ := time.Parse(layout, timeString)
			currentProfile.ValidUntil = validUntil
		}
	}
	if currentProfile != nil && currentProfile.URL != "" {
		profileMap[currentProfile.URL] = currentProfile
	}

	target, exists := profileMap[targetProfile]
	if !exists {
		return false
	}

	return time.Now().Before(target.ValidUntil)
}

func (t *TSH) getProxyFlags() ([]string, error) {
	proxyAddress, err := t.cleanAddress()
	if err != nil {
		return nil, err
	}

	return []string{"--proxy=" + proxyAddress}, nil
}

// authFlags return the authentication flags
func (t *TSH) authFlags() []string {
	var args []string
	if t.proxy.AuthConnector != "" {
		args = append(args, "--auth="+t.proxy.AuthConnector)
	} else {
		args = append(args, "--user="+t.proxy.UserName)
	}
	return args
}

func (t *TSH) cleanAddress() (string, error) {
	u, err := url.Parse(t.proxy.Address)
	if err != nil {
		return "", err
	}
	return u.Host, nil
}

// tshBinary return the location of TSH binary
func (t *TSH) tshBinary() string {
	if t.proxy.TSHPath != "" {
		return t.proxy.TSHPath
	}
	return tshBinary
}

func parseNodesFromString(nodeStr string) config.Node {
	var nodeList []config.Item
	for _, line := range strings.Split(nodeStr, "\n") {

		// remove the header of node table
		// for now on the data will get in table formatting,
		// to support all `tsh` old version
		// because the JSON formatting is only supported by
		// newer TSH
		if strings.HasPrefix(line, "Node") || strings.HasPrefix(line, "---") || strings.HasPrefix(line, " ") {
			continue
		}
		lines := strings.Split(line, " ")

		// infoCount indicate that the node information we want to get has already fulfill
		var infoCount int
		var node config.Item
		for _, s := range lines {
			if s == "" {
				continue
			}
			if infoCount == 2 {
				break
			}
			if infoCount == 0 {
				node.Hostname = s
			} else {
				node.Address = s
			}
			infoCount++
		}
		// doesn't need to append an empty node
		if node != (config.Item{}) {
			nodeList = append(nodeList, node)
		}
	}

	return config.Node{
		Items: nodeList,
	}
}

// NewTSH creates a new TSH
func NewTSH(p *config.Proxy) *TSH {
	return &TSH{
		proxy:   p,
		cmdExec: Command,
		// the minimum version for supporting Status is TSH v2.6.1
		minVersion: Version{
			Major: 2,
			Minor: 6,
			Patch: 1,
		},
	}
}

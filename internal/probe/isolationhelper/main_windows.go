// This executable is an opt-in Windows isolation TEST fixture, not a tool
// exposed by the product. It touches only synthetic paths supplied by tests.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func appContainer() bool {
	var value, length uint32
	err := windows.GetTokenInformation(windows.GetCurrentProcessToken(), 29, (*byte)(unsafe.Pointer(&value)), 4, &length)
	return err == nil && value == 1
}

func containerSID() string {
	var length uint32
	if err := windows.GetTokenInformation(windows.GetCurrentProcessToken(), 31, nil, 0, &length); err != windows.ERROR_INSUFFICIENT_BUFFER || length < 8 || length > 256 {
		return ""
	}
	data := make([]byte, length)
	if err := windows.GetTokenInformation(windows.GetCurrentProcessToken(), 31, &data[0], length, &length); err != nil {
		return ""
	}
	sid := *(**windows.SID)(unsafe.Pointer(&data[0]))
	if sid == nil {
		return ""
	}
	value := sid.String()
	return value
}

func fileBoundaries(work, outside, peer, outputName string) map[string]bool {
	result := map[string]bool{"app_container": appContainer()}
	var elevated, length uint32
	err := windows.GetTokenInformation(windows.GetCurrentProcessToken(), windows.TokenElevation, (*byte)(unsafe.Pointer(&elevated)), 4, &length)
	result["not_elevated"] = err == nil && length == 4 && elevated == 0
	data, err := os.ReadFile(filepath.Join(work, "input.txt"))
	result["read_allowed"] = err == nil && string(data) == "authorized-test-input"
	result["write_allowed"] = os.WriteFile(filepath.Join(work, outputName), []byte("authorized-test-output"), 0600) == nil
	_, err = os.ReadFile(filepath.Join(outside, "synthetic-secret.txt"))
	result["outside_read_denied"] = errors.Is(err, windows.ERROR_ACCESS_DENIED)
	err = os.WriteFile(filepath.Join(outside, outputName), []byte("should-not-be-created"), 0600)
	result["outside_write_denied"] = errors.Is(err, windows.ERROR_ACCESS_DENIED)
	err = os.WriteFile(filepath.Join(filepath.Dir(os.Args[0]), outputName), []byte("should-not-be-created"), 0600)
	result["readonly_bin_write_denied"] = errors.Is(err, windows.ERROR_ACCESS_DENIED)
	_, err = os.ReadFile(filepath.Join(peer, "synthetic-secret.txt"))
	result["peer_read_denied"] = errors.Is(err, windows.ERROR_ACCESS_DENIED)
	err = os.WriteFile(filepath.Join(peer, outputName), []byte("should-not-be-created"), 0600)
	result["peer_write_denied"] = errors.Is(err, windows.ERROR_ACCESS_DENIED)
	data, err = os.ReadFile(filepath.Join(filepath.Dir(os.Args[0]), "common-app-resource.txt"))
	result["common_app_read_denied"] = errors.Is(err, windows.ERROR_ACCESS_DENIED)
	result["common_app_read_allowed"] = err == nil && string(data) == "shared-application-fixture"
	return result
}

func main() {
	if len(os.Args) == 6 && os.Args[1] == "child" {
		result := fileBoundaries(filepath.Dir(os.Args[2]), os.Args[3], os.Args[4], "child-output.txt")
		actual := containerSID()
		result["same_container_sid"] = actual != "" && actual == os.Args[5]
		data, _ := json.Marshal(result)
		if err := os.WriteFile(os.Args[2], data, 0600); err != nil {
			fmt.Fprintf(os.Stderr, "child result write: %v; assertions=%s\n", err, data)
			os.Exit(4)
		}
		return
	}
	if len(os.Args) != 7 {
		os.Exit(2)
	}
	work, outside, tcp4, tcp6, udp4, peer := os.Args[1], os.Args[2], os.Args[3], os.Args[4], os.Args[5], os.Args[6]
	result := fileBoundaries(work, outside, peer, "output.txt")
	networkErrors := make(map[string]string)
	for _, target := range []struct{ name, network, address string }{{"tcp4_denied", "tcp4", tcp4}, {"tcp6_denied", "tcp6", tcp6}, {"udp4_denied", "udp4", udp4}} {
		connection, err := net.DialTimeout(target.network, target.address, 500*time.Millisecond)
		if err == nil {
			_, err = connection.Write([]byte("test-network-boundary"))
			_ = connection.Close()
		}
		result[target.name] = errors.Is(err, windows.WSAEACCES)
		if err == nil {
			networkErrors[target.name] = "connection and write succeeded"
		} else {
			networkErrors[target.name] = err.Error()
		}
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if listener != nil {
		_ = listener.Close()
	}
	result["listen_denied"] = errors.Is(err, windows.WSAEACCES)
	if err == nil {
		networkErrors["listen_denied"] = "listen succeeded"
	} else {
		networkErrors["listen_denied"] = err.Error()
	}
	childResult := filepath.Join(work, "child-result.json")
	command := exec.Command(os.Args[0], "child", childResult, outside, peer, containerSID())
	err = command.Run()
	result["child_runs"] = err == nil
	if err == nil {
		childData, err := os.ReadFile(childResult)
		var child map[string]bool
		if err == nil && json.Unmarshal(childData, &child) == nil {
			result["child_inherits_container"] = child["app_container"] && child["same_container_sid"]
			for _, name := range []string{"not_elevated", "read_allowed", "write_allowed", "outside_read_denied", "outside_write_denied", "readonly_bin_write_denied", "peer_read_denied", "peer_write_denied", "common_app_read_denied", "common_app_read_allowed"} {
				result["child_"+name] = child[name]
			}
		}
	}
	data, err := json.Marshal(result)
	if err != nil {
		os.Exit(3)
	}
	if err := os.WriteFile(filepath.Join(work, "isolation-result.json"), data, 0600); err != nil {
		fmt.Fprintf(os.Stderr, "result write: %v; assertions=%s; network=%v\n", err, data, networkErrors)
		os.Exit(3)
	}
	data, err = json.Marshal(networkErrors)
	if err != nil || os.WriteFile(filepath.Join(work, "isolation-network-errors.json"), data, 0600) != nil {
		os.Exit(3)
	}
}

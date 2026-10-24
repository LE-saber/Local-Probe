// Test-only process fixture. No package in the product imports this program.
package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"
)

func main() {
	if os.Getenv("LP_OPERATIONS_HELPER") != "1" || len(os.Args) != 2 {
		os.Exit(3)
	}
	switch os.Args[1] {
	case "grandchild":
		if os.WriteFile("child.pid", []byte(strconv.Itoa(os.Getpid())), 0600) != nil {
			os.Exit(2)
		}
		for {
			time.Sleep(time.Second)
		}
	case "spawn":
		child := exec.Command(os.Args[0], "grandchild")
		child.Env = os.Environ()
		if child.Start() != nil {
			os.Exit(2)
		}
		end := time.Now().Add(3 * time.Second)
		for {
			if _, err := os.Stat("child.pid"); err == nil {
				break
			}
			if time.Now().After(end) {
				os.Exit(2)
			}
			time.Sleep(time.Millisecond)
		}
		fmt.Println("spawned child without inherited output pipes")
	case "write":
		f, err := os.OpenFile("count.txt", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(2)
		}
		if _, err = f.WriteString("once\n"); err != nil {
			os.Exit(2)
		}
		if err = f.Close(); err != nil {
			os.Exit(2)
		}
		fmt.Println("done")
	case "wait", "wait-two":
		marker := "started.txt"
		if os.Args[1] == "wait-two" {
			marker = "started-two.txt"
		}
		if os.WriteFile(marker, []byte("started"), 0600) != nil {
			os.Exit(2)
		}
		for {
			time.Sleep(time.Second)
		}
	case "flood":
		os.Stdout.Write(bytes.Repeat([]byte("x"), 256<<10))
	case "environment":
		fmt.Printf("secret=%s\n", os.Getenv("LP_UNAPPROVED_SECRET"))
	case "--version":
		fmt.Println("test-tool 1.2.3")
	default:
		os.Exit(3)
	}
}

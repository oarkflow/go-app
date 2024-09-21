package main

import (
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strings"
)

var restrictedCommands = []string{"rm", "sudo", "poweroff", "shutdown", "reboot", "halt"}
var allowDestructiveCommands = false

func isCommandRestricted(cmd string) bool {
	for _, restricted := range restrictedCommands {
		if strings.Contains(cmd, restricted) {
			return true
		}
	}
	return false
}

func executeCommand(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == http.MethodPost {
		err := r.ParseForm()
		if err != nil {
			http.Error(w, "Unable to parse form", http.StatusBadRequest)
			return
		}
		cmd := r.FormValue("command")
		enableDestructive := r.FormValue("enable_destructive")
		if cmd == "" {
			http.Error(w, "No command provided", http.StatusBadRequest)
			return
		}
		if enableDestructive == "true" {
			allowDestructiveCommands = true
		} else {
			allowDestructiveCommands = false
		}
		if !allowDestructiveCommands && isCommandRestricted(cmd) {
			http.Error(w, "Command is restricted for safety", http.StatusForbidden)
			return
		}
		out, err := exec.Command("bash", "-c", cmd).CombinedOutput()
		if err != nil {
			http.Error(w, fmt.Sprintf("Error executing command: %v", err), http.StatusInternalServerError)
			return
		}
		w.Write(out)
	} else {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
	}
}

func main() {
	fs := http.FileServer(http.Dir("./web"))
	http.Handle("/", fs)
	http.HandleFunc("/run-command", executeCommand)
	fmt.Println("Server starting on port 8080...")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

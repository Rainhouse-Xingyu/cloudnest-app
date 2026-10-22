package main

import (
	"errors"
	"io"
	"strconv"
	"strings"

	"cloudenest_go/internal/auth"
)

type maintenanceCommand struct {
	kind     string
	userID   int64
	username string
}

func parseMaintenanceArgs(args []string) (maintenanceCommand, bool, error) {
	if len(args) == 0 {
		return maintenanceCommand{}, false, nil
	}
	switch args[0] {
	case "migrate", "bootstrap-admin", "audit-file-names", "migrate-file-names":
		if len(args) != 1 {
			return maintenanceCommand{}, true, errors.New("maintenance command does not accept extra arguments")
		}
		return maintenanceCommand{kind: args[0]}, true, nil
	case "reset-password":
		if len(args) != 3 {
			return maintenanceCommand{}, true, errors.New("usage: cloudnest reset-password <positive-user-id> <exact-username>; password is read from stdin, never argv")
		}
		id, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || id <= 0 || args[2] == "" {
			return maintenanceCommand{}, true, errors.New("positive user ID and exact username are required")
		}
		return maintenanceCommand{kind: args[0], userID: id, username: args[2]}, true, nil
	default:
		return maintenanceCommand{}, false, nil
	}
}

func readResetPassword(input io.Reader) (string, error) {
	// 72 bcrypt bytes plus optional CRLF. Read one extra byte to reject overflow.
	raw, err := io.ReadAll(io.LimitReader(input, 75))
	if err != nil {
		return "", errors.New("could not read password from stdin")
	}
	password := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
	if err := auth.ValidateNewPassword(password); err != nil {
		return "", err
	}
	return password, nil
}

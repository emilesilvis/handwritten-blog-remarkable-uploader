package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type deviceConfig struct {
	AccessToken string `json:"access_token"`
	DeviceID    string `json:"device_id"`
}

func loadConfig(configPath string) (deviceConfig, error) {
	var configuration deviceConfig
	info, err := os.Stat(configPath)
	if err != nil {
		return configuration, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return configuration, fmt.Errorf("%s must not be readable by other users", configPath)
	}
	contents, err := os.ReadFile(configPath)
	if err != nil {
		return configuration, err
	}
	if err := json.Unmarshal(contents, &configuration); err != nil {
		return configuration, fmt.Errorf("decode device configuration: %w", err)
	}
	if configuration.AccessToken == "" || configuration.DeviceID == "" {
		return configuration, errors.New("the device configuration is incomplete; run purge and link again")
	}
	return configuration, nil
}

func saveConfig(configPath string, configuration deviceConfig) error {
	if configuration.AccessToken == "" || configuration.DeviceID == "" {
		return errors.New("refusing to save an incomplete device configuration")
	}
	directory := filepath.Dir(configPath)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return err
	}
	contents, err := json.Marshal(configuration)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".config-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(contents, '\n')); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, configPath)
}

func idempotencyKey(documentID, contentHash string) string {
	sum := sha256.Sum256([]byte(documentID + ":" + contentHash))
	return hex.EncodeToString(sum[:])
}

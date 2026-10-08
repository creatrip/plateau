package incus

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/creatrip/plateau/internal/domain"
)

func (manager Manager) ensureImage(force bool) error {
	release, err := manager.locks.Acquire("image")
	if err != nil {
		return err
	}
	defer release()
	output, err := manager.transport.Output([]string{"sudo", "incus", "image", "alias", "list", "--format=json"})
	if err != nil {
		return fmt.Errorf("list Incus image aliases: %w", err)
	}
	var aliases []struct {
		Name   string `json:"name"`
		Target string `json:"target"`
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	if err := decoder.Decode(&aliases); err != nil {
		return fmt.Errorf("decode Incus image aliases: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("decode Incus image aliases: trailing data")
	}
	imageSource := "images:debian/13"
	imageScript := containerProvisionScript
	for _, alias := range aliases {
		if alias.Name == managedImageAlias {
			if alias.Target == "" {
				return fmt.Errorf("managed image alias has no target")
			}
			if !force {
				return nil
			}
			imageSource = managedImageAlias
			imageScript = containerManagedImageUpdateScript
		}
	}

	output, err = manager.transport.Output([]string{"sudo", "incus", "list", "^" + imageBuilderName + "$", "--format=json"})
	if err != nil {
		return fmt.Errorf("inspect managed image builder: %w", err)
	}
	var builders []struct {
		Name   string            `json:"name"`
		Type   string            `json:"type"`
		Config map[string]string `json:"config"`
	}
	decoder = json.NewDecoder(bytes.NewReader(output))
	if err := decoder.Decode(&builders); err != nil {
		return fmt.Errorf("decode managed image builder: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF || len(builders) > 1 {
		return fmt.Errorf("validate managed image builder")
	}
	if len(builders) == 1 {
		if builders[0].Name != imageBuilderName || builders[0].Type != "container" || builders[0].Config["user.plateau.image-builder"] != "true" {
			return fmt.Errorf("refuse to replace unmanaged Incus instance %q", imageBuilderName)
		}
		if err := manager.runCommand([]string{"sudo", "incus", "delete", "--force", imageBuilderName}, nil, io.Discard, imageBuilderName, "managed image builder"); err != nil {
			return fmt.Errorf("remove stale managed image builder: %w", err)
		}
	}
	if err := manager.runCommand([]string{
		"sudo", "incus", "init", imageSource, imageBuilderName,
		"--storage", managedStoragePool,
		"--config", "boot.autostart=false",
		"--config", "user.plateau.image-builder=true",
	}, nil, io.Discard, imageBuilderName, "managed image builder"); err != nil {
		return fmt.Errorf("initialize managed image builder: %w", err)
	}
	if err := manager.runCommand([]string{"sudo", "incus", "start", imageBuilderName}, nil, io.Discard, imageBuilderName, "managed image builder"); err != nil {
		_ = manager.transport.Run([]string{"sudo", "incus", "delete", "--force", imageBuilderName}, nil, io.Discard, io.Discard)
		return fmt.Errorf("start managed image builder: %w", err)
	}
	if err := manager.runCommand([]string{"sudo", "incus", "exec", imageBuilderName, "--", "bash", "-s"}, strings.NewReader(imageScript), manager.stdout, imageBuilderName, "managed image builder"); err != nil {
		_ = manager.transport.Run([]string{"sudo", "incus", "delete", "--force", imageBuilderName}, nil, io.Discard, io.Discard)
		return fmt.Errorf("provision managed desktop image: %w", err)
	}
	if err := manager.runCommand([]string{"sudo", "incus", "stop", imageBuilderName}, nil, io.Discard, imageBuilderName, "managed image builder"); err != nil {
		_ = manager.transport.Run([]string{"sudo", "incus", "delete", "--force", imageBuilderName}, nil, io.Discard, io.Discard)
		return fmt.Errorf("stop managed image builder: %w", err)
	}
	if err := manager.runCommand([]string{"sudo", "incus", "publish", imageBuilderName, "--alias", managedImageAlias, "--reuse"}, nil, io.Discard, imageBuilderName, "managed image builder"); err != nil {
		_ = manager.transport.Run([]string{"sudo", "incus", "delete", "--force", imageBuilderName}, nil, io.Discard, io.Discard)
		return fmt.Errorf("publish managed desktop image: %w", err)
	}
	if err := manager.runCommand([]string{"sudo", "incus", "delete", imageBuilderName}, nil, io.Discard, imageBuilderName, "managed image builder"); err != nil {
		return fmt.Errorf("remove managed image builder: %w", err)
	}
	return nil
}

func (manager Manager) UpdateGuest(name domain.Name) error {
	incusName, err := incusInstanceName(name)
	if err != nil {
		return err
	}
	return manager.runCommand([]string{"sudo", "incus", "exec", incusName, "--", "bash", "-s"}, strings.NewReader(containerUpdateScript), manager.stdout, incusName, name.String())
}

func (manager Manager) UpdateImage() error {
	return manager.ensureImage(true)
}

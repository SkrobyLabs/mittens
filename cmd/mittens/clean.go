package main

import (
	"fmt"
	"strings"
)

// runClean removes stopped mittens containers, their DinD volumes, and optionally unused mittens images.
func runClean(args []string) error {
	dryRun, images, err := parseCleanFlags(args)
	if err != nil {
		return err
	}

	// Find stopped mittens containers.
	containers := listStoppedContainers()

	removedContainers := 0
	removedVolumes := 0
	for _, name := range containers {
		if dryRun {
			fmt.Printf("Would remove container: %s\n", name)
		} else {
			fmt.Printf("Removing container: %s\n", name)
			if _, err := captureCommand("docker", "rm", "-f", name); err != nil {
				logWarn("Failed to remove container %s: %v", name, err)
				continue
			}
		}
		removedContainers++

		// Check for associated DinD volume.
		volName := name + "-docker"
		vol, _ := captureCommand("docker", "volume", "ls", "--filter", "name="+volName, "-q")
		if vol != "" {
			if dryRun {
				fmt.Printf("Would remove volume: %s\n", volName)
			} else {
				fmt.Printf("Removing volume: %s\n", volName)
				if _, err := captureCommand("docker", "volume", "rm", volName); err != nil {
					logWarn("Failed to remove volume %s: %v", volName, err)
					continue
				}
			}
			removedVolumes++
		}
	}

	removedImages := 0
	if images {
		imgLines := listMittensImages()
		for _, line := range imgLines {
			// Format: "repository:tag imageID"
			parts := strings.Fields(line)
			if len(parts) < 1 {
				continue
			}
			imgRef := parts[0]
			if dryRun {
				fmt.Printf("Would remove image: %s\n", imgRef)
			} else {
				fmt.Printf("Removing image: %s\n", imgRef)
				if _, err := captureCommand("docker", "rmi", imgRef); err != nil {
					logWarn("Failed to remove image %s: %v", imgRef, err)
					continue
				}
			}
			removedImages++
		}
	}

	action := "Removed"
	if dryRun {
		action = "Would remove"
	}
	fmt.Printf("%s %d containers, %d volumes", action, removedContainers, removedVolumes)
	if images {
		fmt.Printf(", %d images", removedImages)
	}
	fmt.Println()

	return nil
}

// parseCleanFlags extracts --dry-run and --images from the argument list.
func parseCleanFlags(args []string) (dryRun, images bool, err error) {
	for _, a := range args {
		switch a {
		case "--dry-run":
			dryRun = true
		case "--images":
			images = true
		default:
			return false, false, fmt.Errorf("unknown flag %q for \"mittens clean\" (supported: --dry-run, --images)", a)
		}
	}
	return
}

// filterMittensNames returns only names that start with "mittens-".
func filterMittensNames(names []string) []string {
	var out []string
	for _, n := range names {
		if strings.HasPrefix(n, "mittens-") {
			out = append(out, n)
		}
	}
	return out
}

// listStoppedContainers returns names of stopped containers matching "mittens-*".
func listStoppedContainers() []string {
	out, err := captureCommand("docker", "ps", "-a",
		"--filter", "name=^mittens-",
		"--filter", "status=exited",
		"--filter", "status=created",
		"--filter", "status=dead",
		"--format", "{{.Names}}")
	if err != nil || out == "" {
		return nil
	}
	return filterMittensNames(strings.Split(out, "\n"))
}

// listMittensImages returns lines of "repository:tag imageID" for mittens images.
func listMittensImages() []string {
	out, err := captureCommand("docker", "images", "mittens",
		"--format", "{{.Repository}}:{{.Tag}} {{.ID}}")
	if err != nil || out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

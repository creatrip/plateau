package cli

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/creatrip/plateau/internal/application"
	"github.com/creatrip/plateau/internal/domain"
	"github.com/spf13/cobra"
)

type InstanceService interface {
	Create(name string, group *string) error
	SetGroup(name, group string) error
	Rename(oldName, newName string) error
	List() ([]domain.InstanceStatus, error)
	SSH(name string) error
	VNC(name string) error
	Remove(name string, force bool) error
	Stop(name string) error
	Start(name string) error
	Backup(name string) (string, error)
	Restore(path string) (domain.Name, error)
	Update() error
}

type App struct {
	command *cobra.Command
}

func New(version string, bootstrap application.Bootstrap, instances InstanceService, stdout io.Writer, stderr io.Writer) App {
	rootCommand := &cobra.Command{
		Use:           "plateau",
		Short:         "Manage lightweight Incus Debian containers on macOS",
		Version:       version,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(command *cobra.Command, _ []string) error {
			return command.Help()
		},
	}
	rootCommand.SetOut(stdout)
	rootCommand.SetErr(stderr)
	rootCommand.SetVersionTemplate("plateau {{.Version}}\n")
	rootCommand.CompletionOptions.DisableDefaultCmd = true
	ensureHost := func(command *cobra.Command, arguments []string) error {
		// Cobra checks arity first. Reject invalid names before bootstrap can
		// install dependencies or start a VM; restore takes a path, not a name.
		if command.Name() != "restore" && len(arguments) > 0 {
			if _, err := domain.ParseName(arguments[0]); err != nil {
				return err
			}
		}
		if command.Name() == "rename" {
			if _, err := domain.ParseName(arguments[1]); err != nil {
				return err
			}
			if arguments[0] == arguments[1] {
				return fmt.Errorf("new name must differ from the current name")
			}
		}
		if command.Name() == "regroup" && len(arguments) == 2 {
			if err := domain.ValidateGroup(arguments[1]); err != nil {
				return err
			}
		}
		if command.Flags().Changed("group") {
			group, err := command.Flags().GetString("group")
			if err != nil {
				return err
			}
			if err := domain.ValidateGroup(group); err != nil {
				return err
			}
		}
		timed := command.Name() == "backup" || command.Name() == "restore"
		started := time.Now()
		if timed {
			fmt.Fprintln(command.ErrOrStderr(), "실행 환경을 확인하는 중...")
		}
		if err := bootstrap.Run(); err != nil {
			return fmt.Errorf("bootstrap: %w", err)
		}
		if timed {
			fmt.Fprintf(command.ErrOrStderr(), "실행 환경 준비 완료 (%.1f초)\n", time.Since(started).Seconds())
		}
		return nil
	}
	rootCommand.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the build version",
		Args: func(_ *cobra.Command, arguments []string) error {
			if len(arguments) != 0 {
				return fmt.Errorf("version does not accept arguments")
			}
			return nil
		},
		RunE: func(command *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(command.OutOrStdout(), "plateau %s\n", version)
			return err
		},
	})
	createCommand := &cobra.Command{
		Use: "create <name>", Short: "Create and start a new container", Args: cobra.ExactArgs(1), PreRunE: ensureHost,
		RunE: func(command *cobra.Command, arguments []string) error {
			var group *string
			if command.Flags().Changed("group") {
				value, err := command.Flags().GetString("group")
				if err != nil {
					return err
				}
				group = &value
			}
			return instances.Create(arguments[0], group)
		},
	}
	createCommand.Flags().String("group", "", "Assign one free-form group tag")
	rootCommand.AddCommand(createCommand)
	listCommand := &cobra.Command{
		Use:     "ls",
		Short:   "List Plateau containers",
		Args:    cobra.NoArgs,
		PreRunE: ensureHost,
		RunE: func(command *cobra.Command, _ []string) error {
			statuses, err := instances.List()
			if err != nil {
				return err
			}
			sort.Slice(statuses, func(i, j int) bool {
				if statuses[i].Group == statuses[j].Group {
					return statuses[i].Name < statuses[j].Name
				}
				if statuses[i].Group == "" {
					return false
				}
				if statuses[j].Group == "" {
					return true
				}
				return statuses[i].Group < statuses[j].Group
			})
			writer := tabwriter.NewWriter(command.OutOrStdout(), 0, 4, 2, ' ', 0)
			if _, err := fmt.Fprintln(writer, "NAME\tSTATUS\tDISK\tGROUP"); err != nil {
				return err
			}
			for _, status := range statuses {
				size := "-"
				if status.DiskUsageKnown {
					units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
					value := float64(status.DiskUsageBytes)
					unit := 0
					for value >= 1024 && unit < len(units)-1 {
						value /= 1024
						unit++
					}
					if unit == 0 {
						size = fmt.Sprintf("%d B", status.DiskUsageBytes)
					} else {
						size = fmt.Sprintf("%.1f %s", value, units[unit])
					}
				}
				groupLabel := "-"
				if status.Group != "" {
					groupLabel = status.Group
				}
				if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", status.Name, status.RuntimeStatus, size, groupLabel); err != nil {
					return err
				}
			}
			return writer.Flush()
		},
	}
	rootCommand.AddCommand(listCommand)
	regroupCommand := &cobra.Command{
		Use: "regroup <name> <group>", Short: "Set or clear a container's group tag", PreRunE: ensureHost,
		Args: func(command *cobra.Command, arguments []string) error {
			clear, err := command.Flags().GetBool("clear")
			if err != nil {
				return err
			}
			if clear {
				return cobra.ExactArgs(1)(command, arguments)
			}
			if err := cobra.ExactArgs(2)(command, arguments); err != nil {
				return err
			}
			if arguments[1] == "" {
				return fmt.Errorf("use --clear to remove the group tag")
			}
			return nil
		},
		RunE: func(_ *cobra.Command, arguments []string) error {
			group := ""
			if len(arguments) == 2 {
				group = arguments[1]
			}
			return instances.SetGroup(arguments[0], group)
		},
	}
	regroupCommand.Flags().Bool("clear", false, "Remove the group tag")
	rootCommand.AddCommand(regroupCommand)
	rootCommand.AddCommand(&cobra.Command{
		Use: "rename <old-name> <new-name>", Short: "Rename a stopped container", Args: cobra.ExactArgs(2), PreRunE: ensureHost,
		RunE: func(_ *cobra.Command, arguments []string) error { return instances.Rename(arguments[0], arguments[1]) },
	})
	rootCommand.AddCommand(&cobra.Command{
		Use:     "backup <name>",
		Short:   "Back up a container to one file",
		Args:    cobra.ExactArgs(1),
		PreRunE: ensureHost,
		RunE: func(command *cobra.Command, arguments []string) error {
			started := time.Now()
			fmt.Fprintln(command.ErrOrStderr(), "백업을 준비하는 중... 실행 중인 컨테이너는 먼저 중지합니다.")
			path, err := instances.Backup(arguments[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(command.ErrOrStderr(), "백업 완료 (%.1f초)\n", time.Since(started).Seconds())
			_, err = fmt.Fprintln(command.OutOrStdout(), path)
			return err
		},
	})
	rootCommand.AddCommand(&cobra.Command{
		Use:     "restore <file>",
		Short:   "Restore a container from one backup file",
		Args:    cobra.ExactArgs(1),
		PreRunE: ensureHost,
		RunE: func(command *cobra.Command, arguments []string) error {
			started := time.Now()
			name, err := instances.Restore(arguments[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(command.ErrOrStderr(), "복원 완료 (%.1f초)\n", time.Since(started).Seconds())
			_, err = fmt.Fprintln(command.OutOrStdout(), name)
			return err
		},
	})
	for _, lifecycle := range []struct {
		use   string
		short string
		run   func(string) error
	}{
		{use: "ssh <name>", short: "Open an interactive container shell", run: instances.SSH},
		{use: "vnc <name>", short: "Open a container desktop over VNC", run: instances.VNC},
		{use: "stop <name>", short: "Stop a container", run: instances.Stop},
		{use: "start <name>", short: "Start a container", run: instances.Start},
	} {
		rootCommand.AddCommand(&cobra.Command{
			Use:     lifecycle.use,
			Short:   lifecycle.short,
			Args:    cobra.ExactArgs(1),
			PreRunE: ensureHost,
			RunE: func(_ *cobra.Command, arguments []string) error {
				return lifecycle.run(arguments[0])
			},
		})
	}
	rootCommand.AddCommand(&cobra.Command{
		Use:   "update",
		Short: "Update managed dependencies to their latest stable releases",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := bootstrap.Update(); err != nil {
				return err
			}
			return instances.Update()
		},
	})
	removeCommand := &cobra.Command{
		Use:     "rm <name>",
		Short:   "Remove a stopped container",
		Args:    cobra.ExactArgs(1),
		PreRunE: ensureHost,
		RunE: func(command *cobra.Command, arguments []string) error {
			force, err := command.Flags().GetBool("force")
			if err != nil {
				return err
			}
			return instances.Remove(arguments[0], force)
		},
	}
	removeCommand.Flags().BoolP("force", "f", false, "Stop and remove a running container")
	rootCommand.AddCommand(removeCommand)

	return App{command: rootCommand}
}

func (app App) Run(arguments []string) int {
	if arguments == nil {
		arguments = []string{}
	}
	app.command.SetArgs(arguments)

	if err := app.command.Execute(); err != nil {
		var shellExit interface{ ShellExitCode() int }
		if errors.As(err, &shellExit) {
			return shellExit.ShellExitCode()
		}
		_, _ = fmt.Fprintln(app.command.ErrOrStderr(), err)
		return 2
	}
	return 0
}

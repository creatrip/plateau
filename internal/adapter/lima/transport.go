package lima

import "io"

// Host is the only route from the macOS CLI to the shared Linux host.
func (host Host) Run(arguments []string, stdin io.Reader, stdout, stderr io.Writer) error {
	args := append([]string{"shell", plateauHostName, "--"}, arguments...)
	return host.runner.Run("limactl", args, stdin, stdout, stderr)
}

func (host Host) Output(arguments []string) ([]byte, error) {
	args := append([]string{"shell", plateauHostName, "--"}, arguments...)
	return host.runner.Output("limactl", args)
}

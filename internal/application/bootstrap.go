package application

type HostProvisioner interface {
	Ensure() error
	Update() error
}

type Bootstrap struct {
	locks Locker
	hosts []HostProvisioner
}

func NewBootstrap(locks Locker, hosts ...HostProvisioner) Bootstrap {
	return Bootstrap{hosts: hosts, locks: locks}
}

func (bootstrap Bootstrap) Run() error {
	release, err := bootstrap.locks.Acquire("bootstrap")
	if err != nil {
		return err
	}
	defer release()
	for _, host := range bootstrap.hosts {
		if err := host.Ensure(); err != nil {
			return err
		}
	}
	return nil
}

func (bootstrap Bootstrap) Update() error {
	release, err := bootstrap.locks.Acquire("bootstrap")
	if err != nil {
		return err
	}
	defer release()
	for _, host := range bootstrap.hosts {
		if err := host.Update(); err != nil {
			return err
		}
	}
	return nil
}

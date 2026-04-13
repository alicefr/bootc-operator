package podman

type ContainerCreateOptions struct {
	Name        string
	Image       string
	Network     string
	Devices     []string
	Volumes     []string
	Ports       []string
	Environment map[string]string
}

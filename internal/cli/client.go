package cli

import (
	"github.com/samansohani78/SixSync/internal/apisix"
	"github.com/samansohani78/SixSync/internal/buildinfo"
	"github.com/samansohani78/SixSync/internal/config"
	"github.com/samansohani78/SixSync/internal/exitcode"
)

// newClient builds an Admin API client from resolved configuration.
func newClient(cfg config.Config) (*apisix.Client, error) {
	if err := cfg.RequireAdminURL(); err != nil {
		return nil, exitcode.Wrap(exitcode.Usage, err)
	}
	c, err := apisix.New(apisix.Options{
		BaseURL:            cfg.AdminURL,
		APIKey:             cfg.AdminKey,
		Timeout:            cfg.Timeout,
		CAFile:             cfg.CAFile,
		ClientCertFile:     cfg.ClientCertFile,
		ClientKeyFile:      cfg.ClientKeyFile,
		InsecureSkipVerify: cfg.InsecureSkipVerify,
		UserAgent:          "sixsync/" + buildinfo.Get().Version,
	})
	if err != nil {
		return nil, exitcode.Wrap(exitcode.Usage, err)
	}
	return c, nil
}

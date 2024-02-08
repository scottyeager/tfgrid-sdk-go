package parser

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/gliderlabs/ssh"
	"github.com/go-playground/validator/v10"
	"github.com/rs/zerolog/log"
	deployer "github.com/threefoldtech/tfgrid-sdk-go/mass-deployer/pkg/mass-deployer"
	"gopkg.in/yaml.v3"
)

const (
	mnemonicKey = "MNEMONIC"
	networkKey  = "NETWORK"
)

func ParseConfig(file io.Reader, jsonFmt bool) (deployer.Config, error) {
	conf := deployer.Config{}

	configFile, err := io.ReadAll(file)
	if err != nil {
		return deployer.Config{}, fmt.Errorf("failed to read the config file: %+w", err)
	}
	if jsonFmt {
		err = json.Unmarshal(configFile, &conf)
	} else {
		err = yaml.Unmarshal(configFile, &conf)
	}
	if err != nil {
		return deployer.Config{}, err
	}

	conf.Mnemonic, err = getValueOrEnv(conf.Mnemonic, mnemonicKey)
	if err != nil {
		return deployer.Config{}, err
	}

	conf.Network, err = getValueOrEnv(conf.Network, networkKey)
	if err != nil {
		return deployer.Config{}, err
	}

	if err := validateNetwork(conf.Network); err != nil {
		return deployer.Config{}, err
	}

	if err := validateMnemonic(conf.Mnemonic); err != nil {
		return deployer.Config{}, err
	}

	for _, nodeGroup := range conf.NodeGroups {
		nodeGroupName := strings.TrimSpace(nodeGroup.Name)
		if !alphanumeric.MatchString(nodeGroupName) {
			return deployer.Config{}, fmt.Errorf("node group name: '%s' is invalid, should be lowercase alphanumeric and underscore only", nodeGroupName)
		}
	}

	return conf, nil
}

func ValidateConfig(conf deployer.Config) error {
	log.Info().Msg("validating configuration file")

	v := validator.New(validator.WithRequiredStructEnabled())
	if err := v.Struct(conf); err != nil {
		err = parseValidationError(err)
		return err
	}

	for name, sshKey := range conf.SSHKeys {
		_, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(sshKey)))
		if err != nil {
			return fmt.Errorf("ssh key for `%s` is invalid: %+w", name, err)
		}
	}

	if err := validateVMs(conf.Vms, conf.NodeGroups, conf.SSHKeys); err != nil {
		return err
	}

	log.Info().Msg("done validating configuration file")
	return nil
}

func getValueOrEnv(value, envKey string) (string, error) {
	envKey = strings.ToUpper(envKey)
	if len(strings.TrimSpace(value)) == 0 {
		value = os.Getenv(envKey)
		if len(strings.TrimSpace(value)) == 0 {
			return "", fmt.Errorf("could not find valid %s", envKey)
		}
	}
	return value, nil
}

func parseValidationError(err error) error {
	if _, ok := err.(*validator.InvalidValidationError); ok {
		return err
	}

	for _, err := range err.(validator.ValidationErrors) {
		tag := err.Tag()
		value := err.Value()
		boundary := err.Param()
		nameSpace := err.Namespace()

		switch tag {
		case "required":
			return fmt.Errorf("field '%s' should not be empty", nameSpace)
		case "max":
			return fmt.Errorf("value of '%s': '%v' is out of range, max value is '%s'", nameSpace, value, boundary)
		case "min":
			return fmt.Errorf("value of '%s': '%v' is out of range, min value is '%s'", nameSpace, value, boundary)
		}
	}
	return nil
}

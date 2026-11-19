package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestExecSecretsLoad(t *testing.T) {
	c, err := Load(writeTemp(t, minimal+
		"exec_secrets:\n  PYPI_TOKEN:\n    commands: [\"twine upload\", \"uv publish\"]\n  GH_TOKEN:\n    commands: [\"gh release\"]\n"),
		mapEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]ExecSecret{
		"PYPI_TOKEN": {Commands: []string{"twine upload", "uv publish"}},
		"GH_TOKEN":   {Commands: []string{"gh release"}},
	}
	if !reflect.DeepEqual(c.ExecSecrets, want) {
		t.Fatalf("got %+v", c.ExecSecrets)
	}
	if d := Default(); d.ExecSecrets == nil || len(d.ExecSecrets) != 0 {
		t.Fatal("the default map must be empty, not nil")
	}
}

func TestValidateExecSecrets(t *testing.T) {
	for _, tc := range []struct {
		secrets map[string]ExecSecret
		pass    []string
		want    string
	}{
		{map[string]ExecSecret{"PYPI_TOKEN": {Commands: []string{"twine upload"}}}, []string{"PYPI_TOKEN"},
			"config: exec_secrets.PYPI_TOKEN is also in terminal.env_passthrough. Remove it from one list."},
		{map[string]ExecSecret{"PYPI_TOKEN": {}}, nil,
			`config: exec_secrets.PYPI_TOKEN.commands is empty. Add at least one command, for example "twine upload"`},
		{map[string]ExecSecret{"PYPI_TOKEN": {Commands: []string{"twine upload", "  "}}}, nil,
			`config: exec_secrets.PYPI_TOKEN.commands[1] has no words. Write a program and its first words, for example "twine upload"`},
		{map[string]ExecSecret{"1PYPI": {Commands: []string{"twine upload"}}}, nil,
			`config: exec_secrets key "1PYPI" is not a valid .env name. Use letters, digits, and "_", and do not start with a digit`},
		{map[string]ExecSecret{"PYPI-TOKEN": {Commands: []string{"twine upload"}}}, nil,
			`config: exec_secrets key "PYPI-TOKEN" is not a valid .env name. Use letters, digits, and "_", and do not start with a digit`},
	} {
		c := Default()
		c.Providers["primary"] = Provider{Format: "openai"}
		c.ExecSecrets = tc.secrets
		c.Terminal.EnvPassthrough = tc.pass
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("want %q, got %v", tc.want, err)
		}
	}
	// The case of a name is free, and an absolute path entry is valid.
	c := Default()
	c.Providers["primary"] = Provider{Format: "openai"}
	c.ExecSecrets = map[string]ExecSecret{
		"pypi_token": {Commands: []string{"twine upload"}},
		"_X1":        {Commands: []string{"/opt/tools/deploy --prod"}},
	}
	c.Terminal.EnvPassthrough = []string{"HELIUS_API_KEY"}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

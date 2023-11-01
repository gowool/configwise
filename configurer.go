// MIT License
//
// Copyright (c) 2022 Spiral Scout
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

package configwise

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mitchellh/mapstructure"
	"github.com/spf13/viper"
)

var (
	_ Configurer = (*configurer)(nil)

	TagName = "cfg"
)

const (
	OpNew          = "configurer: new ->"
	OpUnmarshalKey = "configurer: unmarshal key ->"
	OpUnmarshal    = "configurer: unmarshal ->"
	OpOverwrite    = "configurer: overwrite ->"
	OpParseFlag    = "configurer: parse flag ->"
)

type Configurer interface {
	// UnmarshalKey takes a single key and unmarshal it into a Struct.
	UnmarshalKey(name string, out interface{}) error

	// Unmarshal the config into a Struct. Make sure that the tags
	// on the fields of the structure are properly set.
	Unmarshal(out interface{}) error

	// Overwrite used to overwrite particular values in the unmarshalled config
	Overwrite(values map[string]interface{}) error

	// Get used to get config section
	Get(name string) interface{}

	// Has checks if config section exists.
	Has(name string) bool
}

type Option func(*configurer)

type configurer struct {
	viper     *viper.Viper
	path      string
	prefix    string
	tp        string
	readInCfg []byte
	// user defined Flags in the form of <option>.<key> = <value>
	// which overwrites initial config key
	flags []string
}

func WithPath(path string) Option {
	return func(c *configurer) {
		c.path = path
	}
}

func WithPrefix(prefix string) Option {
	return func(c *configurer) {
		c.prefix = prefix
	}
}

func WithConfigType(tp string) Option {
	return func(c *configurer) {
		c.tp = tp
	}
}

func WithReadInCfg(readInCfg []byte) Option {
	return func(c *configurer) {
		c.readInCfg = readInCfg
	}
}

func WithFlags(flags []string) Option {
	return func(c *configurer) {
		c.flags = flags
	}
}

func NewConfigurer(options ...Option) (Configurer, error) {
	c := &configurer{viper: viper.New()}

	for _, opt := range options {
		opt(c)
	}

	// If user provided []byte data with config, read it and ignore Path and Prefix
	if c.readInCfg != nil && c.tp != "" {
		c.viper.SetConfigType(c.tp)
		err := c.viper.ReadConfig(bytes.NewBuffer(c.readInCfg))
		return c, err
	}

	// read in environment variables that match
	c.viper.AutomaticEnv()
	if c.prefix == "" {
		return nil, fmt.Errorf("%s prefix should be set", OpNew)
	}

	c.viper.SetEnvPrefix(c.prefix)
	c.viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))

	if c.path == "" {
		ex, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("%s %w", OpNew, err)
		}
		c.viper.AddConfigPath(filepath.Dir(ex))
		c.viper.AddConfigPath(filepath.Join("/", "etc", filepath.Base(ex)))
	} else {
		c.viper.SetConfigFile(c.path)
	}

	err := c.viper.ReadInConfig()
	if err != nil {
		return nil, fmt.Errorf("%s %w", OpNew, err)
	}

	// automatically inject ENV variables using ${ENV} pattern
	for _, key := range c.viper.AllKeys() {
		val := c.viper.Get(key)
		switch t := val.(type) {
		case string:
			// for string just expand it
			c.viper.Set(key, parseEnvDefault(t))
		case []interface{}:
			// for slice -> check if it's slice of strings
			strArr := make([]string, 0, len(t))
			for i := 0; i < len(t); i++ {
				if valStr, ok := t[i].(string); ok {
					strArr = append(strArr, parseEnvDefault(valStr))
					continue
				}

				c.viper.Set(key, val)
			}

			// we should set the whole array
			if len(strArr) > 0 {
				c.viper.Set(key, strArr)
			}
		default:
			c.viper.Set(key, val)
		}
	}

	// override config flags
	for _, f := range c.flags {
		key, val, errP := parseFlag(f)
		if errP != nil {
			return nil, fmt.Errorf("%s %w", OpNew, errP)
		}
		c.viper.Set(key, parseEnvDefault(val))
	}

	return c, nil
}

func (cfg *configurer) UnmarshalKey(name string, out interface{}) error {
	if err := cfg.viper.UnmarshalKey(name, out, decoderConfig); err != nil {
		return fmt.Errorf("%s %w", OpUnmarshalKey, err)
	}
	return nil
}

func (cfg *configurer) Unmarshal(out interface{}) error {
	if err := cfg.viper.Unmarshal(out, decoderConfig); err != nil {
		return fmt.Errorf("%s %w", OpUnmarshal, err)
	}
	return nil
}

func (cfg *configurer) Overwrite(values map[string]interface{}) error {
	for key, value := range values {
		cfg.viper.Set(key, value)
	}
	return nil
}

func (cfg *configurer) Get(name string) interface{} {
	return cfg.viper.Get(name)
}

func (cfg *configurer) Has(name string) bool {
	return cfg.viper.IsSet(name)
}

func parseFlag(flag string) (string, string, error) {
	if !strings.Contains(flag, "=") {
		return "", "", fmt.Errorf("%s invalid flag `%s`", OpParseFlag, flag)
	}

	parts := strings.SplitN(strings.TrimLeft(flag, " \"'`"), "=", 2)
	if len(parts) < 2 {
		return "", "", errors.New("usage: -o key=value")
	}

	if parts[0] == "" {
		return "", "", errors.New("key should not be empty")
	}

	if parts[1] == "" {
		return "", "", errors.New("value should not be empty")
	}

	return strings.Trim(parts[0], " \n\t"), parseValue(strings.Trim(parts[1], " \n\t")), nil
}

func parseValue(value string) string {
	escape := []rune(value)[0]

	if escape == '"' || escape == '\'' || escape == '`' {
		value = strings.Trim(value, string(escape))
		value = strings.ReplaceAll(value, fmt.Sprintf("\\%s", string(escape)), string(escape))
	}

	return value
}

func parseEnvDefault(val string) string {
	// tcp://127.0.0.1:${RPC_PORT:-36643}
	// for envs like this, part would be tcp://127.0.0.1:
	return ExpandVal(val, os.Getenv)
}

func decoderConfig(config *mapstructure.DecoderConfig) {
	config.TagName = TagName
	config.DecodeHook = mapstructure.ComposeDecodeHookFunc(
		stringToUUID,
		mapstructure.StringToTimeHookFunc(time.RFC3339),
		mapstructure.StringToTimeDurationHookFunc(),
		mapstructure.StringToSliceHookFunc(","),
	)
}

func stringToUUID(from reflect.Type, to reflect.Type, data interface{}) (interface{}, error) {
	if from.Kind() != reflect.String || to != reflect.TypeOf(uuid.Nil) {
		return data, nil
	}
	return uuid.Parse(data.(string))
}

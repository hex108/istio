//  Copyright 2018 Istio Authors
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

package galley

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/hashicorp/go-multierror"
	"istio.io/istio/galley/pkg/server"
	"istio.io/istio/pkg/test/framework2/components/environment/native"
	"istio.io/istio/pkg/test/framework2/resource"
	"istio.io/istio/pkg/test/scopes"
)

var (
	_ Instance  = &nativeComponent{}
	_ io.Closer = &nativeComponent{}
)

const (
	galleyWorkdir  = "galley-workdir"
	configDir      = "config"
	meshConfigDir  = "mesh-config"
	meshConfigFile = "meshconfig.yaml"
)

// NewNativeComponent factory function for the component
func newNative(s resource.Context, e *native.Environment) (Instance, error) {

	n := &nativeComponent{
		context:     s,
		environment: e,
	}

	s.TrackResource(n)
	return n, n.Reset()
}

type nativeComponent struct {
	context     resource.Context
	environment *native.Environment

	client *client

	// Top level home dir for all configuration that is fed to Galley
	homeDir string

	// The folder that Galley reads to local, file-based configuration from
	configDir string

	// The folder that Galley reads the mesh config file from
	meshConfigDir string

	// The file that Galley reads the mesh config file from.
	meshConfigFile string

	server *server.Server
}

// Address of the Galley MCP Server.
func (c *nativeComponent) Address() string {
	return c.client.address
}

// SetMeshConfig applies the given mesh config yaml file via Galley.
func (c *nativeComponent) SetMeshConfig(yamlText string) error {
	if err := ioutil.WriteFile(c.meshConfigFile, []byte(yamlText), os.ModePerm); err != nil {
		return err
	}
	if err := c.Close(); err != nil {
		return err
	}

	return c.restart()
}

// ClearConfig implements Galley.ClearConfig.
func (c *nativeComponent) ClearConfig() (err error) {
	infos, err := ioutil.ReadDir(c.configDir)
	if err != nil {
		return err
	}
	for _, i := range infos {
		err := os.Remove(path.Join(c.configDir, i.Name()))
		if err != nil {
			return err
		}
	}

	return
}

// ApplyConfig implements Galley.ApplyConfig.
func (c *nativeComponent) ApplyConfig(yamlText string) (err error) {
	fn := fmt.Sprintf("cfg-%d.yaml", time.Now().UnixNano())
	fn = path.Join(c.configDir, fn)

	if err = ioutil.WriteFile(fn, []byte(yamlText), os.ModePerm); err != nil {
		return err
	}

	return
}

// ApplyConfigDir implements Galley.ApplyConfigDir.
func (c *nativeComponent) ApplyConfigDir(sourceDir string) (err error) {
	return filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
		targetPath := c.configDir + string(os.PathSeparator) + path[len(sourceDir):]
		if info.IsDir() {
			scopes.Framework.Debugf("Making dir: %v", targetPath)
			return os.MkdirAll(targetPath, os.ModePerm)
		}
		scopes.Framework.Debugf("Copying file to: %v", targetPath)
		contents, readerr := ioutil.ReadFile(path)
		if readerr != nil {
			return readerr
		}
		return ioutil.WriteFile(targetPath, contents, os.ModePerm)
	})
}

// WaitForSnapshot implements Galley.WaitForSnapshot.
func (c *nativeComponent) WaitForSnapshot(collection string, snapshot ...map[string]interface{}) error {
	return c.client.waitForSnapshot(collection, snapshot)
}

// Reset implements Resettable.Reset.
func (c *nativeComponent) Reset() error {
	_ = c.Close()

	var err error
	if c.homeDir, err = c.context.CreateTmpDirectory(galleyWorkdir); err != nil {
		scopes.Framework.Errorf("Error creating config directory for Galley: %v", err)
		return err
	}
	scopes.Framework.Debugf("Galley home dir: %v", c.homeDir)

	c.configDir = path.Join(c.homeDir, configDir)
	if err = os.MkdirAll(c.configDir, os.ModePerm); err != nil {
		return err
	}
	scopes.Framework.Debugf("Galley config dir: %v", c.configDir)

	c.meshConfigDir = path.Join(c.homeDir, meshConfigDir)
	if err = os.MkdirAll(c.meshConfigDir, os.ModePerm); err != nil {
		return err
	}
	scopes.Framework.Debugf("Galley mesh config dir: %v", c.meshConfigDir)

	c.meshConfigFile = path.Join(c.meshConfigDir, meshConfigFile)
	if err = ioutil.WriteFile(c.meshConfigFile, []byte{}, os.ModePerm); err != nil {
		return err
	}

	return c.restart()
}

func (c *nativeComponent) restart() error {
	a := server.DefaultArgs()
	a.Insecure = true
	a.EnableServer = true
	a.DisableResourceReadyCheck = true
	a.ConfigPath = c.configDir
	a.MeshConfigFile = c.meshConfigFile
	// To prevent ctrlZ port collision between galley/pilot&mixer
	a.IntrospectionOptions.Port = 0
	a.ExcludedResourceKinds = make([]string, 0)

	// Bind to an arbitrary port.
	a.APIAddress = "tcp://0.0.0.0:0"

	s, err := server.New(a)
	if err != nil {
		scopes.Framework.Errorf("Error starting Galley: %v", err)
		return err
	}

	c.server = s

	go s.Run()

	c.client = &client{
		address: fmt.Sprintf("tcp://%s", s.Address().String()),
	}

	if err = c.client.waitForStartup(); err != nil {
		return err
	}

	return nil
}

// Close implements io.Closer.
func (c *nativeComponent) Close() (err error) {
	if c.client != nil {
		err = c.client.Close()
		c.client = nil
	}
	if c.server != nil {
		err = multierror.Append(c.server.Close()).ErrorOrNil()
		if err != nil {
			scopes.Framework.Infof("Error while Galley server close during reset: %v", err)
		}
		c.server = nil
	}
	return
}
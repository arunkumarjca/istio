// Copyright Istio Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package stackdriver

import (
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"time"

	environ "istio.io/istio/pkg/test/env"
	"istio.io/istio/pkg/test/framework/components/environment/kube"
	"istio.io/istio/pkg/test/framework/components/namespace"
	"istio.io/istio/pkg/test/framework/resource"
	testKube "istio.io/istio/pkg/test/kube"
	"istio.io/istio/pkg/test/scopes"

	jsonpb "github.com/golang/protobuf/jsonpb"
	ltype "google.golang.org/genproto/googleapis/logging/type"
	loggingpb "google.golang.org/genproto/googleapis/logging/v2"
	monitoringpb "google.golang.org/genproto/googleapis/monitoring/v3"
)

const (
	stackdriverNamespace = "istio-stackdriver"
	stackdriverPort      = 8091
)

var (
	_ Instance  = &kubeComponent{}
	_ io.Closer = &kubeComponent{}
)

type kubeComponent struct {
	id        resource.ID
	ns        namespace.Instance
	forwarder testKube.PortForwarder
	cluster   kube.Cluster
}

func newKube(ctx resource.Context, cfg Config) (Instance, error) {
	c := &kubeComponent{
		cluster: kube.ClusterOrDefault(cfg.Cluster, ctx.Environment()),
	}
	c.id = ctx.TrackResource(c)
	var err error
	scopes.CI.Info("=== BEGIN: Deploy Stackdriver ===")
	defer func() {
		if err != nil {
			err = fmt.Errorf("stackdriver deployment failed: %v", err) // nolint:golint
			scopes.CI.Infof("=== FAILED: Deploy Stackdriver ===")
			_ = c.Close()
		} else {
			scopes.CI.Info("=== SUCCEEDED: Deploy Stackdriver ===")
		}
	}()

	c.ns, err = namespace.New(ctx, namespace.Config{
		Prefix: stackdriverNamespace,
	})
	if err != nil {
		return nil, fmt.Errorf("could not create %s Namespace for Stackdriver install; err:%v", stackdriverNamespace, err)
	}

	// apply stackdriver YAML
	yamlContent, err := ioutil.ReadFile(environ.StackdriverInstallFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s, err: %v", environ.StackdriverInstallFilePath, err)
	}

	if _, err := c.cluster.ApplyContents(c.ns.Name(), string(yamlContent)); err != nil {
		return nil, fmt.Errorf("failed to apply rendered %s, err: %v", environ.StackdriverInstallFilePath, err)
	}

	fetchFn := c.cluster.NewSinglePodFetch(c.ns.Name(), "app=stackdriver")
	pods, err := c.cluster.WaitUntilPodsAreReady(fetchFn)
	if err != nil {
		return nil, err
	}
	pod := pods[0]

	forwarder, err := c.cluster.NewPortForwarder(pod, 0, stackdriverPort)
	if err != nil {
		return nil, err
	}

	if err := forwarder.Start(); err != nil {
		return nil, err
	}
	c.forwarder = forwarder
	scopes.Framework.Debugf("initialized stackdriver port forwarder: %v", forwarder.Address())

	return c, nil
}

func (c *kubeComponent) ListTimeSeries() ([]*monitoringpb.TimeSeries, error) {
	client := http.Client{
		Timeout: 5 * time.Second,
	}
	resp, err := client.Get("http://" + c.forwarder.Address() + "/timeseries")
	if err != nil {
		return []*monitoringpb.TimeSeries{}, err
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return []*monitoringpb.TimeSeries{}, err
	}
	var r monitoringpb.ListTimeSeriesResponse
	err = jsonpb.UnmarshalString(string(body), &r)
	if err != nil {
		return []*monitoringpb.TimeSeries{}, err
	}
	var ret []*monitoringpb.TimeSeries
	for _, t := range r.TimeSeries {
		// Remove fields that do not need verification
		t.Points = nil
		t.Resource = nil
		ret = append(ret, t)
	}
	return ret, nil
}

func (c *kubeComponent) ListLogEntries() ([]*loggingpb.LogEntry, error) {
	client := http.Client{
		Timeout: 5 * time.Second,
	}
	resp, err := client.Get("http://" + c.forwarder.Address() + "/logentries")
	if err != nil {
		return []*loggingpb.LogEntry{}, err
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return []*loggingpb.LogEntry{}, err
	}
	var r loggingpb.ListLogEntriesResponse
	err = jsonpb.UnmarshalString(string(body), &r)
	if err != nil {
		return []*loggingpb.LogEntry{}, err
	}
	var ret []*loggingpb.LogEntry
	for _, l := range r.Entries {
		// Remove fields that do not need verification
		l.Timestamp = nil
		l.Severity = ltype.LogSeverity_DEFAULT
		l.HttpRequest.ResponseSize = 0
		l.HttpRequest.RequestSize = 0
		l.HttpRequest.ServerIp = ""
		l.HttpRequest.RemoteIp = ""
		l.HttpRequest.Latency = nil
		delete(l.Labels, "request_id")
		delete(l.Labels, "source_name")
		delete(l.Labels, "destination_name")
		ret = append(ret, l)
	}
	return ret, nil
}

func (c *kubeComponent) ID() resource.ID {
	return c.id
}

// Close implements io.Closer.
func (c *kubeComponent) Close() error {
	return nil
}

func (c *kubeComponent) GetStackdriverNamespace() string {
	return c.ns.Name()
}

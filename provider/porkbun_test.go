/*
Copyright 2022 The Kubernetes Authors.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package porkbun

import (
	"context"
	"os"
	"testing"

	pb "github.com/nrdcg/porkbun"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"

	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/external-dns/endpoint"
	"sigs.k8s.io/external-dns/plan"
)

func TestPorkbunProvider(t *testing.T) {
	t.Run("EndpointZoneName", testEndpointZoneName)
	t.Run("GetIDforRecord", testGetIDforRecord)
	t.Run("ConvertToNetcupRecord", testConvertToPorkbunRecord)
	t.Run("NewNetcupProvider", testNewPorkbunProvider)
	t.Run("ApplyChanges", testApplyChanges)
	t.Run("Records", testRecords)
}

func testEndpointZoneName(t *testing.T) {
	zoneList := []string{"bar.org", "baz.org"}

	// in zone list
	ep1 := endpoint.Endpoint{
		DNSName:    "foo.bar.org",
		Targets:    endpoint.Targets{"5.5.5.5"},
		RecordType: endpoint.RecordTypeA,
	}

	// not in zone list
	ep2 := endpoint.Endpoint{
		DNSName:    "foo.foo.org",
		Targets:    endpoint.Targets{"5.5.5.5"},
		RecordType: endpoint.RecordTypeA,
	}

	// matches zone exactly
	ep3 := endpoint.Endpoint{
		DNSName:    "baz.org",
		Targets:    endpoint.Targets{"5.5.5.5"},
		RecordType: endpoint.RecordTypeA,
	}

	assert.Equal(t, endpointZoneName(&ep1, zoneList), "bar.org")
	assert.Equal(t, endpointZoneName(&ep2, zoneList), "")
	assert.Equal(t, endpointZoneName(&ep3, zoneList), "baz.org")
}

func testGetIDforRecord(t *testing.T) {

	recordName := "foo.example.com"
	target1 := "heritage=external-dns,external-dns/owner=default,external-dns/resource=service/default/nginx"
	target2 := "5.5.5.5"
	recordType := "TXT"

	nc1 := pb.Record{
		Name:    "foo.example.com",
		Type:    "TXT",
		Content: "heritage=external-dns,external-dns/owner=default,external-dns/resource=service/default/nginx",
		ID:      "10",
	}
	nc2 := pb.Record{
		Name:    "foo.foo.org",
		Type:    "A",
		Content: "5.5.5.5",
		ID:      "10",
	}

	nc3 := pb.Record{
		ID:      "",
		Name:    "baz.org",
		Type:    "A",
		Content: "5.5.5.5",
	}

	ncRecordList := []pb.Record{nc1, nc2, nc3}

	assert.Equal(t, "10", getIDforRecord(recordName, target1, recordType, &ncRecordList))
	assert.Equal(t, "", getIDforRecord(recordName, target2, recordType, &ncRecordList))

}

func testConvertToPorkbunRecord(t *testing.T) {
	// in zone list
	ep1 := endpoint.Endpoint{
		DNSName:    "foo.bar.org",
		Targets:    endpoint.Targets{"5.5.5.5"},
		RecordType: endpoint.RecordTypeA,
	}

	// not in zone list
	ep2 := endpoint.Endpoint{
		DNSName:    "foo.foo.org",
		Targets:    endpoint.Targets{"5.5.5.5"},
		RecordType: endpoint.RecordTypeA,
	}

	// matches zone exactly
	ep3 := endpoint.Endpoint{
		DNSName:    "bar.org",
		Targets:    endpoint.Targets{"5.5.5.5"},
		RecordType: endpoint.RecordTypeA,
	}

	ep4 := endpoint.Endpoint{
		DNSName:    "foo.baz.org",
		Targets:    endpoint.Targets{"\"heritage=external-dns,external-dns/owner=default,external-dns/resource=service/default/nginx\""},
		RecordType: endpoint.RecordTypeTXT,
	}

	epList := []*endpoint.Endpoint{&ep1, &ep2, &ep3, &ep4}

	nc1 := pb.Record{
		Name:    "foo",
		Type:    "A",
		Content: "5.5.5.5",
		ID:      "10",
	}
	nc2 := pb.Record{
		Name:    "foo.foo.org",
		Type:    "A",
		Content: "5.5.5.5",
		ID:      "15",
	}

	nc3 := pb.Record{
		ID:      "",
		Name:    "@",
		Type:    "A",
		Content: "5.5.5.5",
	}

	nc4 := pb.Record{
		ID:      "",
		Name:    "foo.baz.org",
		Type:    "TXT",
		Content: "heritage=external-dns,external-dns/owner=default,external-dns/resource=service/default/nginx",
	}

	ncRecordList := []pb.Record{nc1, nc2, nc3, nc4}

	// No deletion
	assert.Equal(t, convertToPorkbunRecord(&ncRecordList, epList, "bar.org", false), &ncRecordList)
}

func testNewPorkbunProvider(t *testing.T) {
	domainFilter := []string{"example.com"}
	var logger log.Logger
	logger = log.NewLogfmtLogger(log.NewSyncWriter(os.Stderr))
	logger = level.NewFilter(logger, level.Allow(level.InfoValue()))
	logger = log.With(logger, "ts", log.DefaultTimestampUTC, "caller", log.DefaultCaller)

	p, err := NewPorkbunProvider(&domainFilter, "KEY", "PASSWORD", true, logger)
	assert.NotNil(t, p.client)
	assert.NoError(t, err)

	_, err = NewPorkbunProvider(&domainFilter, "", "PASSWORD", true, logger)
	assert.Error(t, err)

	_, err = NewPorkbunProvider(&domainFilter, "KEY", "", true, logger)
	assert.Error(t, err)

	emptyDomainFilter := []string{}
	_, err = NewPorkbunProvider(&emptyDomainFilter, "KEY", "PASSWORD", true, logger)
	assert.Error(t, err)

}

func testApplyChanges(t *testing.T) {
	domainFilter := []string{"example.com"}
	var logger log.Logger
	logger = log.NewLogfmtLogger(log.NewSyncWriter(os.Stderr))
	logger = level.NewFilter(logger, level.Allow(level.InfoValue()))
	logger = log.With(logger, "ts", log.DefaultTimestampUTC, "caller", log.DefaultCaller)

	p, _ := NewPorkbunProvider(&domainFilter, "KEY", "PASSWORD", true, logger)
	changes1 := &plan.Changes{
		Create:    []*endpoint.Endpoint{},
		Delete:    []*endpoint.Endpoint{},
		UpdateNew: []*endpoint.Endpoint{},
		UpdateOld: []*endpoint.Endpoint{},
	}

	// No Changes
	err := p.ApplyChanges(context.TODO(), changes1)
	assert.NoError(t, err)

	// Changes
	changes2 := &plan.Changes{
		Create: []*endpoint.Endpoint{
			{
				DNSName:    "api.example.com",
				RecordType: "A",
			},
			{
				DNSName:    "api.baz.com",
				RecordType: "TXT",
			}},
		Delete: []*endpoint.Endpoint{
			{
				DNSName:    "api.example.com",
				RecordType: "A",
			},
			{
				DNSName:    "api.baz.com",
				RecordType: "TXT",
			}},
		UpdateNew: []*endpoint.Endpoint{
			{
				DNSName:    "api.example.com",
				RecordType: "A",
			},
			{
				DNSName:    "api.baz.com",
				RecordType: "TXT",
			}},
		UpdateOld: []*endpoint.Endpoint{
			{
				DNSName:    "api.example.com",
				RecordType: "A",
			},
			{
				DNSName:    "api.baz.com",
				RecordType: "TXT",
			}},
	}

	err = p.ApplyChanges(context.TODO(), changes2)
	assert.NoError(t, err)

}

func testRecords(t *testing.T) {
	domainFilter := []string{"example.com"}
	var logger log.Logger
	logger = log.NewLogfmtLogger(log.NewSyncWriter(os.Stderr))
	logger = level.NewFilter(logger, level.Allow(level.InfoValue()))
	logger = log.With(logger, "ts", log.DefaultTimestampUTC, "caller", log.DefaultCaller)

	p, _ := NewPorkbunProvider(&domainFilter, "KEY", "PASSWORD", true, logger)
	ep, err := p.Records(context.TODO())
	assert.Equal(t, []*endpoint.Endpoint{}, ep)
	assert.NoError(t, err)
}

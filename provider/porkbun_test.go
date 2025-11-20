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
	"io"
	"log/slog"
	"testing"

	pb "github.com/nrdcg/porkbun"
	"github.com/prometheus/common/promslog"

	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/external-dns/endpoint"
	"sigs.k8s.io/external-dns/plan"
)

func TestPorkbunProvider(t *testing.T) {
	t.Run("EndpointZoneName", testEndpointZoneName)
	t.Run("GetIDforRecordStrict", testGetIDforRecordStrict)
	t.Run("GetIDforRecordNonStrict", testGetIDforRecordNonStrict)
	t.Run("ConvertToPorkbunRecord", testConvertToPorkbunRecord)
	t.Run("NewPorkbunProvider", testNewPorkbunProvider)
	t.Run("ApplyChanges", testApplyChanges)
	t.Run("Records", testRecords)
	t.Run("RemoveNoopTXTUpdates", testRemoveNoopTXTUpdates)

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

	assert.Equal(t, "bar.org", endpointZoneName(&ep1, zoneList))
	assert.Equal(t, "", endpointZoneName(&ep2, zoneList))
	assert.Equal(t, "baz.org", endpointZoneName(&ep3, zoneList))
}

func testGetIDforRecordStrict(t *testing.T) {

	recordName := "foo.example.com"
	target1 := "heritage=external-dns,external-dns/owner=default,external-dns/resource=service/default/nginx"
	target2 := "5.5.5.5"
	recordType := "TXT"

	// Porkbun API returns fully qualified names in the Name field.
	// e.g. for "foo.example.com" it returns Name: "foo.example.com"
	pb1 := pb.Record{
		Name:    "foo.example.com", // FQDN — matches recordName
		Type:    "TXT",
		Content: "heritage=external-dns,external-dns/owner=default,external-dns/resource=service/default/nginx",
		ID:      "10",
	}
	pb2 := pb.Record{
		Name:    "foo.foo.org",
		Type:    "A",
		Content: "5.5.5.5",
		ID:      "10",
	}

	pb3 := pb.Record{
		ID:      "",
		Name:    "baz.org",
		Type:    "A",
		Content: "5.5.5.5",
	}

	pbRecordList := []pb.Record{pb1, pb2, pb3}

	assert.Equal(t, "10", getIDforRecord(recordName, target1, recordType, pbRecordList, true))
	assert.Equal(t, "", getIDforRecord(recordName, "", recordType, pbRecordList, true))
	assert.Equal(t, "", getIDforRecord(recordName, target2, recordType, pbRecordList, true))

	// Root record: zone equals recordName — both should be "example.com"
	rootRecord := pb.Record{
		Name:    "example.com", // FQDN for root record
		Type:    "A",
		Content: "1.2.3.4",
		ID:      "42",
	}
	rootList := []pb.Record{rootRecord}
	assert.Equal(t, "42", getIDforRecord("example.com", "1.2.3.4", "A", rootList, true))

	// Cross-zone record: record from different zone should not match
	assert.Equal(t, "", getIDforRecord("other.zone.org", "5.5.5.5", "A", pbRecordList, true))
}

func testGetIDforRecordNonStrict(t *testing.T) {
	recordName := "foo.example.com"
	recordType := "TXT"

	pb1 := pb.Record{
		Name:    "foo.example.com",
		Type:    "TXT",
		Content: "value-1",
		ID:      "10",
	}
	pb2 := pb.Record{
		Name:    "foo.example.com",
		Type:    "TXT",
		Content: "value-2",
		ID:      "11",
	}
	pb3 := pb.Record{
		Name:    "bar.example.com",
		Type:    "TXT",
		Content: "value-3",
		ID:      "12",
	}
	pb4 := pb.Record{
		Name:    "foo.example.com",
		Type:    "A",
		Content: "1.2.3.4",
		ID:      "13",
	}

	pbRecordList := []pb.Record{pb1, pb2, pb3, pb4}

	// Non-strict: return first id
	id := getIDforRecord(recordName, "some-other-value", recordType, pbRecordList, false)
	if id != "10" {
		t.Fatalf("expected non-strict getIDforRecord to return first matching ID '10', got '%s'", id)
	}

	unknownID := getIDforRecord("does.not.exist", "whatever", recordType, pbRecordList, false)
	if unknownID != "" {
		t.Fatalf("expected empty ID for unknown record name, got '%s'", unknownID)
	}

	// Non-strict with a name+type match (content ignored): the A record wins
	wrongTypeID := getIDforRecord(recordName, "value-1", "A", pbRecordList, false)
	if wrongTypeID != "13" {
		t.Fatalf("expected non-strict getIDforRecord to match by name+type and return '13', got '%s'", wrongTypeID)
	}
}

func testConvertToPorkbunRecord(t *testing.T) {
	// in zone list
	ep1 := endpoint.Endpoint{
		DNSName:    "foo.bar.org",
		Targets:    endpoint.Targets{"5.5.5.5"},
		RecordType: endpoint.RecordTypeA,
		RecordTTL:  600, // forced minimum
	}

	// not in zone list
	ep2 := endpoint.Endpoint{
		DNSName:    "foo.foo.org",
		Targets:    endpoint.Targets{"5.5.5.5"},
		RecordType: endpoint.RecordTypeA,
		RecordTTL:  600, // forced minimum
	}

	// matches zone exactly
	ep3 := endpoint.Endpoint{
		DNSName:    "bar.org",
		Targets:    endpoint.Targets{"5.5.5.5"},
		RecordType: endpoint.RecordTypeA,
		RecordTTL:  600, // forced minimum
	}

	ep4 := endpoint.Endpoint{
		DNSName:    "baz.org",
		Targets:    endpoint.Targets{"\"heritage=external-dns,external-dns/owner=default,external-dns/resource=service/default/nginx\""},
		RecordType: endpoint.RecordTypeTXT,
		RecordTTL:  600, // forced minimum
	}

	epList := []*endpoint.Endpoint{&ep1, &ep2, &ep3, &ep4}

	// Porkbun API returns fully qualified names in the Name field.
	// e.g. for "foo.bar.org" it returns Name: "foo.bar.org"
	pb1Retrieved := pb.Record{
		Name:    "foo.bar.org", // FQDN — matches ep1.DNSName
		Type:    "A",
		Content: "5.5.5.5",
		ID:      "10",
		TTL:     "600",
	}
	pb1 := pb.Record{
		Name:    "foo",
		Type:    "A",
		Content: "5.5.5.5",
		ID:      "10",
		TTL:     "600",
	}
	pb2 := pb.Record{
		Name:    "foo.foo.org",
		Type:    "A",
		Content: "5.5.5.5",
		ID:      "15",
		TTL:     "600",
	}
	pb3retrieved := pb.Record{
		ID:      "1",
		Name:    "bar.org", // FQDN for root record
		Type:    "A",
		Content: "5.5.5.5",
		TTL:     "600",
	}
	pb3 := pb.Record{
		ID:      "1",
		Name:    "", // empty for API call (root record)
		Type:    "A",
		Content: "5.5.5.5",
		TTL:     "600",
	}
	pb4 := pb.Record{
		ID:      "",
		Name:    "baz.org",
		Type:    "TXT",
		Content: "heritage=external-dns,external-dns/owner=default,external-dns/resource=service/default/nginx",
		TTL:     "600",
	}

	// The retrieved records use FQDN names (as Porkbun API returns)
	pbRetrievedRecordList := []pb.Record{pb1Retrieved, pb2, pb3retrieved}

	// ep4 (baz.org) is NOT in the retrieved list (different zone), so ID should be ""
	pbRecordList := []pb.Record{pb1, pb2, pb3, pb4}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	assert.Equal(t, pbRecordList, convertToPorkbunRecord(logger, pbRetrievedRecordList, epList, "bar.org", false))
}

func testNewPorkbunProvider(t *testing.T) {
	domainFilter := []string{"example.com"}
	var logger *slog.Logger
	promslogConfig := &promslog.Config{}
	logger = promslog.New(promslogConfig)

	p, err := NewPorkbunProvider(domainFilter, "KEY", "PASSWORD", true, logger)
	assert.NotNil(t, p.client)
	assert.NoError(t, err)

	_, err = NewPorkbunProvider(domainFilter, "", "PASSWORD", true, logger)
	assert.Error(t, err)

	_, err = NewPorkbunProvider(domainFilter, "KEY", "", true, logger)
	assert.Error(t, err)

	emptyDomainFilter := []string{}
	_, err = NewPorkbunProvider(emptyDomainFilter, "KEY", "PASSWORD", true, logger)
	assert.Error(t, err)

}

func testApplyChanges(t *testing.T) {
	domainFilter := []string{"example.com"}
	var logger *slog.Logger
	promslogConfig := &promslog.Config{}
	logger = promslog.New(promslogConfig)

	p, _ := NewPorkbunProvider(domainFilter, "KEY", "PASSWORD", true, logger)
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
	var logger *slog.Logger
	promslogConfig := &promslog.Config{}
	logger = promslog.New(promslogConfig)

	p, _ := NewPorkbunProvider(domainFilter, "KEY", "PASSWORD", true, logger)
	ep, err := p.Records(context.TODO())
	assert.Equal(t, []*endpoint.Endpoint{}, ep)
	assert.NoError(t, err)
}

func testRemoveNoopTXTUpdates(t *testing.T) {
	oldNoop := &endpoint.Endpoint{
		DNSName:    "txt-noop.example.com",
		RecordType: endpoint.RecordTypeTXT,
		Targets:    endpoint.Targets{"v=1"},
		RecordTTL:  300,
	}

	// New: same, remove
	newNoop := &endpoint.Endpoint{
		DNSName:    "txt-noop.example.com",
		RecordType: endpoint.RecordTypeTXT,
		Targets:    endpoint.Targets{"v=1"},
		RecordTTL:  300,
	}

	// ttl changed, keep
	oldTTL := &endpoint.Endpoint{
		DNSName:    "txt-ttl.example.com",
		RecordType: endpoint.RecordTypeTXT,
		Targets:    endpoint.Targets{"v=1"},
		RecordTTL:  300,
	}
	newTTL := &endpoint.Endpoint{
		DNSName:    "txt-ttl.example.com",
		RecordType: endpoint.RecordTypeTXT,
		Targets:    endpoint.Targets{"v=1"},
		RecordTTL:  600,
	}

	// Target changed, different key keep
	oldTarget := &endpoint.Endpoint{
		DNSName:    "txt-target.example.com",
		RecordType: endpoint.RecordTypeTXT,
		Targets:    endpoint.Targets{"old"},
		RecordTTL:  300,
	}
	newTarget := &endpoint.Endpoint{
		DNSName:    "txt-target.example.com",
		RecordType: endpoint.RecordTypeTXT,
		Targets:    endpoint.Targets{"new"},
		RecordTTL:  300,
	}

	// no old, keep
	newOrphan := &endpoint.Endpoint{
		DNSName:    "txt-orphan.example.com",
		RecordType: endpoint.RecordTypeTXT,
		Targets:    endpoint.Targets{"v=1"},
		RecordTTL:  300,
	}

	// keep non-txt
	oldA := &endpoint.Endpoint{
		DNSName:    "a.example.com",
		RecordType: endpoint.RecordTypeA,
		Targets:    endpoint.Targets{"1.2.3.4"},
		RecordTTL:  300,
	}
	newA := &endpoint.Endpoint{
		DNSName:    "a.example.com",
		RecordType: endpoint.RecordTypeA,
		Targets:    endpoint.Targets{"1.2.3.4"},
		RecordTTL:  300,
	}

	changes := &plan.Changes{
		UpdateOld: []*endpoint.Endpoint{
			oldNoop,
			oldTTL,
			oldTarget,
			oldA,
		},
		UpdateNew: []*endpoint.Endpoint{
			newNoop,   // remove
			newTTL,    // keep
			newTarget, // keep
			newOrphan, // keep
			newA,      // keep
		},
	}

	removeNoopTXTUpdates(changes)

	if len(changes.UpdateNew) != 4 {
		t.Fatalf("expected 4 UpdateNew entries after removeNoopTXTUpdates, got %d", len(changes.UpdateNew))
	}

	hasEndpoint := func(name, recordType string) bool {
		for _, ep := range changes.UpdateNew {
			if ep.DNSName == name && ep.RecordType == recordType {
				return true
			}
		}
		return false
	}

	// remove
	if hasEndpoint("txt-noop.example.com", endpoint.RecordTypeTXT) {
		t.Errorf("expected txt-noop.example.com TXT to be removed as no-op, but it is still present")
	}

	// keep
	if !hasEndpoint("txt-ttl.example.com", endpoint.RecordTypeTXT) {
		t.Errorf("expected txt-ttl.example.com TXT to remain")
	}
	if !hasEndpoint("txt-target.example.com", endpoint.RecordTypeTXT) {
		t.Errorf("expected txt-target.example.com TXT to remain")
	}
	if !hasEndpoint("txt-orphan.example.com", endpoint.RecordTypeTXT) {
		t.Errorf("expected txt-orphan.example.com TXT to remain")
	}
	if !hasEndpoint("a.example.com", endpoint.RecordTypeA) {
		t.Errorf("expected a.example.com A record to remain")
	}
}

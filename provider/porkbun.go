package porkbun

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	pb "github.com/nrdcg/porkbun"

	"sigs.k8s.io/external-dns/endpoint"
	"sigs.k8s.io/external-dns/plan"
	"sigs.k8s.io/external-dns/provider"
)

// PorkbunProvider is an implementation of Provider for porkbun DNS.
type PorkbunProvider struct {
	provider.BaseProvider
	client       *pb.Client
	domainFilter endpoint.DomainFilter
	dryRun       bool
	logger       *slog.Logger
}

// PorkbunChange includes the changesets that need to be applied to the porkbun API
type PorkbunChange struct {
	Create    *[]pb.Record
	UpdateNew *[]pb.Record
	UpdateOld *[]pb.Record
	Delete    *[]pb.Record
}

// NewPorkbunProvider creates a new provider including the porkbun API client
func NewPorkbunProvider(domainFilterList *[]string, apiKey string, apiSecret string, dryRun bool, logger *slog.Logger) (*PorkbunProvider, error) {
	domainFilter := endpoint.NewDomainFilter(*domainFilterList)

	if !domainFilter.IsConfigured() {
		return nil, fmt.Errorf("porkbun provider requires at least one configured domain in the domainFilter")
	}

	if apiKey == "" {
		return nil, fmt.Errorf("porkbun provider requires an API Key")
	}

	if apiSecret == "" {
		return nil, fmt.Errorf("porkbun provider requires an API Password")
	}

	logger.Debug("creating porkbun provider", "api-key", apiKey, "api-secret", apiSecret)

	client := pb.New(apiSecret, apiKey)

	return &PorkbunProvider{
		client:       client,
		domainFilter: domainFilter,
		dryRun:       dryRun,
		logger:       logger,
	}, nil
}

func (p *PorkbunProvider) CreateDnsRecords(ctx context.Context, zone string, records *[]pb.Record) (string, error) {
	for _, record := range *records {
		_, err := p.client.CreateRecord(ctx, zone, record)
		if err != nil {
			return "", fmt.Errorf("unable to create record: %v", err)
		}
	}
	return "", nil
}

func (p *PorkbunProvider) DeleteDnsRecords(ctx context.Context, zone string, records *[]pb.Record) (string, error) {
	for _, record := range *records {
		id, err := strconv.Atoi(record.ID)
		if err != nil {
			return "", fmt.Errorf("unable to parse record ID '%s': %v. Full record: %+v", record.ID, err, record)
		}
		err = p.client.DeleteRecord(ctx, zone, id)
		if err != nil {
			return "", fmt.Errorf("unable to delete record: %v", err)
		}
	}
	return "", nil
}

func (p *PorkbunProvider) UpdateDnsRecords(ctx context.Context, zone string, records *[]pb.Record) (string, error) {
	for _, record := range *records {
		id, err := strconv.Atoi(record.ID)
		if err != nil {
			return "", fmt.Errorf("unable to parse record ID '%s': %v. Full record: %+v", record.ID, err, record)
		}
		err = p.client.EditRecord(ctx, zone, id, record)
		if err != nil {
			return "", fmt.Errorf("unable to update record: %v", err)
		}
	}
	return "", nil
}

// Records delivers the list of Endpoint records for all zones.
func (p *PorkbunProvider) Records(ctx context.Context) ([]*endpoint.Endpoint, error) {
	endpoints := make([]*endpoint.Endpoint, 0)

	if p.dryRun {
		p.logger.Debug("dry run - skipping login")
	} else {
		err := p.ensureLogin(ctx)
		if err != nil {
			return nil, err
		}

		for _, domain := range p.domainFilter.Filters {

			records, err := p.client.RetrieveRecords(ctx, domain)
			if err != nil {
				return nil, fmt.Errorf("unable to query DNS zone records for domain '%v': %v", domain, err)
			}
			p.logger.Info("got DNS records for domain", "domain", domain)
			for _, rec := range records {
				name := rec.Name
				nameStart := strings.Split(rec.Name, ".")[0]
				if nameStart == "@" {
					name = domain
				}
				ttl, err := strconv.Atoi(rec.TTL)
				if err != nil {
					return nil, fmt.Errorf("unable to parse TTL value: %v", err)
				}
				ep := endpoint.NewEndpointWithTTL(name, rec.Type, endpoint.TTL(ttl), rec.Content)
				endpoints = append(endpoints, ep)
			}
		}
	}
	for _, endpointItem := range endpoints {
		p.logger.Debug("endpoints collected", "endpoints", endpointItem.String())
	}
	return endpoints, nil
}

// ApplyChanges applies a given set of changes in a given zone.
func (p *PorkbunProvider) ApplyChanges(ctx context.Context, changes *plan.Changes) error {
	if !changes.HasChanges() {
		p.logger.Debug("no changes detected - nothing to do")
		return nil
	}

	if p.dryRun {
		p.logger.Debug("dry run - skipping login")
	} else {
		err := p.ensureLogin(ctx)
		if err != nil {
			return err
		}
	}
	perZoneChanges := map[string]*plan.Changes{}

	for _, zoneName := range p.domainFilter.Filters {
		p.logger.Debug("zone detected", "zone", zoneName)

		perZoneChanges[zoneName] = &plan.Changes{}
	}

	for _, ep := range changes.Create {
		zoneName := endpointZoneName(ep, p.domainFilter.Filters)
		if zoneName == "" {
			p.logger.Debug("ignoring change since it did not match any zone", "type", "create", "endpoint", ep)
			continue
		}
		p.logger.Debug("planning", "type", "create", "endpoint", ep, "zone", zoneName)

		perZoneChanges[zoneName].Create = append(perZoneChanges[zoneName].Create, ep)
	}

	for _, ep := range changes.UpdateOld {
		zoneName := endpointZoneName(ep, p.domainFilter.Filters)
		if zoneName == "" {
			p.logger.Debug("ignoring change since it did not match any zone", "type", "updateOld", "endpoint", ep)
			continue
		}
		p.logger.Debug("planning", "type", "updateOld", "endpoint", ep, "zone", zoneName)

		perZoneChanges[zoneName].UpdateOld = append(perZoneChanges[zoneName].UpdateOld, ep)
	}

	for _, ep := range changes.UpdateNew {
		zoneName := endpointZoneName(ep, p.domainFilter.Filters)
		if zoneName == "" {
			p.logger.Debug("ignoring change since it did not match any zone", "type", "updateNew", "endpoint", ep)
			continue
		}
		p.logger.Debug("planning", "type", "updateNew", "endpoint", ep, "zone", zoneName)
		perZoneChanges[zoneName].UpdateNew = append(perZoneChanges[zoneName].UpdateNew, ep)
	}

	for _, ep := range changes.Delete {
		zoneName := endpointZoneName(ep, p.domainFilter.Filters)
		if zoneName == "" {
			p.logger.Debug("ignoring change since it did not match any zone", "type", "delete", "endpoint", ep)
			continue
		}
		p.logger.Debug("planning", "type", "delete", "endpoint", ep, "zone", zoneName)
		perZoneChanges[zoneName].Delete = append(perZoneChanges[zoneName].Delete, ep)
	}

	if p.dryRun {
		p.logger.Info("dry run - not applying changes")
		return nil
	}

	// Assemble changes per zone and prepare it for the porkbun API client
	for zoneName, c := range perZoneChanges {
		// Gather records from API to extract the record ID which is necessary for updating/deleting the record
		recs, err := p.client.RetrieveRecords(ctx, zoneName)
		if err != nil {
			p.logger.Error("unable to get DNS records for domain", "zone", zoneName, "error", err.Error())
		}
		change := &PorkbunChange{
			Create:    convertToPorkbunRecord(&recs, c.Create, zoneName, false),
			UpdateNew: convertToPorkbunRecord(&recs, c.UpdateNew, zoneName, false),
			UpdateOld: convertToPorkbunRecord(&recs, c.UpdateOld, zoneName, true),
			Delete:    convertToPorkbunRecord(&recs, c.Delete, zoneName, true),
		}

		p.logger.Debug("applying changes", "zone", zoneName, "changes", change)

		// If not in dry run, apply changes
		_, err = p.UpdateDnsRecords(ctx, zoneName, change.UpdateOld)
		if err != nil {
			return err
		}
		_, err = p.DeleteDnsRecords(ctx, zoneName, change.Delete)
		if err != nil {
			return err
		}
		_, err = p.CreateDnsRecords(ctx, zoneName, change.Create)
		if err != nil {
			return err
		}
		_, err = p.UpdateDnsRecords(ctx, zoneName, change.UpdateNew)
		if err != nil {
			return err
		}
	}

	p.logger.Debug("update completed")

	return nil
}

// convertToPorkbunRecord transforms a list of endpoints into a list of Porkbun DNS Records
// returns a pointer to a list of DNS Records
func convertToPorkbunRecord(recs *[]pb.Record, endpoints []*endpoint.Endpoint, zoneName string, DeleteRecord bool) *[]pb.Record {
	records := make([]pb.Record, len(endpoints))

	for i, ep := range endpoints {
		recordName := strings.TrimSuffix(ep.DNSName, "."+zoneName)
		if recordName == zoneName {
			recordName = "@"
		}
		target := ep.Targets[0]
		if ep.RecordType == endpoint.RecordTypeTXT && strings.HasPrefix(target, "\"heritage=") {
			target = strings.Trim(ep.Targets[0], "\"")
		}

		records[i] = pb.Record{
			Type:    ep.RecordType,
			Name:    recordName,
			Content: target,
			ID:      getIDforRecord(recordName, target, ep.RecordType, recs),
		}
	}
	return &records
}

// getIDforRecord compares the endpoint with existing records to get the ID from Porkbun to ensure it can be safely removed.
// returns empty string if no match found
func getIDforRecord(recordName string, target string, recordType string, recs *[]pb.Record) string {
	for _, rec := range *recs {
		if recordType == rec.Type && target == rec.Content && rec.Name == recordName {
			return rec.ID
		}
	}

	return ""
}

// endpointZoneName determines zoneName for endpoint by taking longest suffix zoneName match in endpoint DNSName
// returns empty string if no match found
func endpointZoneName(endpoint *endpoint.Endpoint, zones []string) (zone string) {
	var matchZoneName = ""
	for _, zoneName := range zones {
		if strings.HasSuffix(endpoint.DNSName, zoneName) && len(zoneName) > len(matchZoneName) {
			matchZoneName = zoneName
		}
	}
	return matchZoneName
}

// ensureLogin makes sure that we are logged in to Porkbun API.
func (p *PorkbunProvider) ensureLogin(ctx context.Context) error {
	p.logger.Debug("performing login to Porkbun API")
	_, err := p.client.Ping(ctx)
	if err != nil {
		return err
	}
	p.logger.Debug("successfully logged in to Porkbun API")
	return nil
}

package porkbun

import (
	"context"
	"encoding/json"
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
	Create             []pb.Record
	DesiredAfterUpdate []pb.Record
	Delete             []pb.Record
}

// NewPorkbunProvider creates a new provider including the porkbun API client
func NewPorkbunProvider(domainFilterList []string, apiKey string, apiSecret string, dryRun bool, logger *slog.Logger) (*PorkbunProvider, error) {
	if logger == nil {
		return nil, fmt.Errorf("porkbun provider requires a non-nil logger")
	}
	domainFilter := endpoint.NewDomainFilter(domainFilterList)

	if !domainFilter.IsConfigured() {
		return nil, fmt.Errorf("porkbun provider requires at least one configured domain in the domainFilter")
	}

	if apiKey == "" {
		return nil, fmt.Errorf("porkbun provider requires an API Key")
	}

	if apiSecret == "" {
		return nil, fmt.Errorf("porkbun provider requires an API Password")
	}

	logger.Debug("creating porkbun provider", "domains", domainFilterList, "dry-run", dryRun)

	client := pb.New(apiSecret, apiKey)

	return &PorkbunProvider{
		client:       client,
		domainFilter: *domainFilter,
		dryRun:       dryRun,
		logger:       logger,
	}, nil
}

func (p *PorkbunProvider) DeleteDnsRecords(ctx context.Context, zone string, records []pb.Record) error {
	for _, record := range records {
		id, err := strconv.Atoi(record.ID)
		if err != nil {
			return fmt.Errorf("unable to parse record ID '%s': %w. Full record: %+v", record.ID, err, record)
		}
		err = p.client.DeleteRecord(ctx, zone, id)
		if err != nil {
			return fmt.Errorf("unable to delete record: %w", err)
		}
	}
	return nil
}

func (p *PorkbunProvider) CreateDnsRecords(ctx context.Context, zone string, records []pb.Record) error {
	for _, record := range records {
		_, err := p.client.CreateRecord(ctx, zone, record)
		if err != nil {
			return fmt.Errorf("unable to create record: %w", err)
		}
	}
	return nil
}

func (p *PorkbunProvider) UpdateDnsRecords(ctx context.Context, zone string, records []pb.Record) error {
	for _, record := range records {
		id, err := strconv.Atoi(record.ID)
		if err != nil {
			return fmt.Errorf("unable to parse record ID '%s': %w. Full record: %+v", record.ID, err, record)
		}
		err = p.client.EditRecord(ctx, zone, id, record)
		if err != nil {
			j, _ := json.MarshalIndent(record, "", "  ")
			return fmt.Errorf("unable to update record %s with id %d at zone %s: %w", j, id, zone, err)
		}
	}
	return nil
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

			recs, err := p.client.RetrieveRecords(ctx, domain)
			if err != nil {
				p.logger.Error("unable to query DNS zone records", "domain", domain, "error", err)
				continue
			}
			p.logger.Info("got DNS records for domain", "domain", domain)
			for _, rec := range recs {
				// Porkbun API returns fully qualified names in the Name field
				// e.g. "foo.example.com", "example.com" for root, "*.example.com" for wildcard
				name := rec.Name
				ttl, err := strconv.Atoi(rec.TTL)
				if err != nil {
					p.logger.Warn("unable to parse TTL, using default", "ttl", rec.TTL, "error", err)
					ttl = 600
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

	// UpdateOld contains the state before the desired update (UpdateNew)
	// see https://github.com/kubernetes-sigs/external-dns/blob/master/plan/plan.go 232 - 233
	for _, ep := range changes.UpdateOld {
		zoneName := endpointZoneName(ep, p.domainFilter.Filters)
		if zoneName == "" {
			p.logger.Debug("ignoring change since it did not match any zone", "type", "updateOld", "endpoint", ep)
			continue
		}
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

	var firstErr error

	// Assemble changes per zone and prepare it for the porkbun API client
	for zoneName, c := range perZoneChanges {
		if !c.HasChanges() {
			continue
		}
		err := applyChangesToZone(ctx, c, p, zoneName)
		if err != nil {
			p.logger.Error("unable to apply changes to zone, skipping", "zone", zoneName, "error", err.Error())
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
	}

	p.logger.Debug("update(s) completed")

	return firstErr
}

func applyChangesToZone(ctx context.Context, c *plan.Changes, p *PorkbunProvider, zone string) error {
	removeNoopTXTUpdates(c)
	if len(c.Create)+len(c.Delete)+len(c.UpdateNew) == 0 {
		return nil
	}

	// Gather records from API to extract the record ID which is necessary for updating/deleting the record
	recs, err := p.client.RetrieveRecords(ctx, zone)
	if err != nil {
		return fmt.Errorf("unable to get DNS records: %w", err)
	}

	change := &PorkbunChange{
		Create:             convertToPorkbunRecord(p.logger, recs, c.Create, zone, false),
		DesiredAfterUpdate: convertToPorkbunRecord(p.logger, recs, c.UpdateNew, zone, false),
		Delete:             convertToPorkbunRecord(p.logger, recs, c.Delete, zone, true),
	}

	err = p.DeleteDnsRecords(ctx, zone, change.Delete)
	if err != nil {
		return fmt.Errorf("unable to delete records: %w", err)
	}
	err = p.CreateDnsRecords(ctx, zone, change.Create)
	if err != nil {
		return fmt.Errorf("unable to create records: %w", err)
	}
	err = p.UpdateDnsRecords(ctx, zone, change.DesiredAfterUpdate)
	if err != nil {
		return fmt.Errorf("unable to update records: %w", err)
	}

	return nil
}

// convertToPorkbunRecord transforms a list of endpoints into a list of Porkbun DNS records.
func convertToPorkbunRecord(logger *slog.Logger, recs []pb.Record, endpoints []*endpoint.Endpoint, zoneName string, useStrictMatchForDelete bool) []pb.Record {
	records := make([]pb.Record, 0, len(endpoints))

	for _, ep := range endpoints {
		fqdn := ep.DNSName

		var recordName string
		if fqdn == zoneName {
			recordName = ""
		} else {
			recordName = strings.TrimSuffix(fqdn, "."+zoneName)
		}

		if len(ep.Targets) == 0 {
			logger.Debug("endpoint has no targets, skipping", "dnsName", ep.DNSName, "type", ep.RecordType)
			continue
		}

		target := ep.Targets[0]
		if ep.RecordType == endpoint.RecordTypeTXT && strings.HasPrefix(target, "\"heritage=") {
			target = strings.Trim(ep.Targets[0], "\"")
		}

		var id = getIDforRecord(fqdn, target, ep.RecordType, recs, useStrictMatchForDelete)

		records = append(records, pb.Record{
			Type:    ep.RecordType,
			Name:    recordName, // e.g. subsub.sub
			TTL:     strconv.FormatInt(int64(ep.RecordTTL), 10),
			Content: target,
			ID:      id, // ID from FQDN-Match
		})
	}
	return records
}

// removeNoopTXTUpdates removes unchanged TXT updates from UpdateNew.
// UpdateOld is left as-is and is not used in further processing.
func removeNoopTXTUpdates(c *plan.Changes) {
	if len(c.UpdateOld) == 0 || len(c.UpdateNew) == 0 {
		return
	}

	type key struct {
		name   string
		target string
	}

	// Map: (DNSName + Content) -> old Endpoint
	oldMap := make(map[key]*endpoint.Endpoint)

	for _, ep := range c.UpdateOld {
		if ep.RecordType != endpoint.RecordTypeTXT {
			continue
		}
		if len(ep.Targets) == 0 {
			continue
		}
		k := key{name: ep.DNSName, target: ep.Targets[0]}
		oldMap[k] = ep
	}

	// Filter UpdateNew: TXT only keep, if sth. changed
	filtered := make([]*endpoint.Endpoint, 0, len(c.UpdateNew))

	for _, ep := range c.UpdateNew {
		// handle only txt
		if ep.RecordType != endpoint.RecordTypeTXT || len(ep.Targets) == 0 {
			filtered = append(filtered, ep)
			continue
		}

		k := key{name: ep.DNSName, target: ep.Targets[0]}
		if old, ok := oldMap[k]; ok {
			// if TTL and Content same -> No-Op, skip
			if old.RecordTTL == ep.RecordTTL {
				continue
			}
		}

		filtered = append(filtered, ep)
	}

	c.UpdateNew = filtered
}

func getIDforRecord(recordName string, target string, recordType string, recs []pb.Record, useStrictMatchForDelete bool) string {
	for _, rec := range recs {
		if rec.Type != recordType || rec.Name != recordName {
			continue
		}
		if useStrictMatchForDelete && target != rec.Content {
			continue
		}
		return rec.ID
	}
	// useful for debugging
	// logger.Debug("no id found for", "recordName", recordName, "target", target, "recordType", recordType)
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

package aws

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/overad9658/tagherd/internal/model"
)

// baseFields are selected for every AWS Config row.
const baseFields = "resourceId, resourceName, resourceType, awsRegion, accountId, arn, resourceCreationTime, tags"

// maxExpression keeps each query under AWS Config's 4,096-character limit.
const maxExpression = 4000

// configProps are the configuration properties read for the catalog's
// discriminators, by native type. Types listed here get a query of their
// own, so a property one type lacks never breaks another type's query.
var configProps = map[string][]string{
	"AWS::RDS::DBInstance":                      {"engine", "dBClusterIdentifier"},
	"AWS::RDS::DBCluster":                       {"engine"},
	"AWS::ElasticLoadBalancingV2::LoadBalancer": {"type"},
}

// buildQueries returns the advanced queries for natives. account, when set,
// restricts an aggregator query to one account.
func buildQueries(natives []string, account string) []string {
	where := ""
	if account != "" {
		where = fmt.Sprintf("accountId = '%s' AND ", account)
	}
	var plain []string
	var out []string
	for _, nt := range natives {
		props, ok := configProps[nt]
		if !ok {
			plain = append(plain, nt)
			continue
		}
		fields := baseFields
		for _, p := range props {
			fields += ", configuration." + p
		}
		out = append(out, fmt.Sprintf("SELECT %s WHERE %sresourceType = '%s'", fields, where, nt))
	}
	// Batch the rest into IN lists that fit the length limit.
	prefix := fmt.Sprintf("SELECT %s WHERE %sresourceType IN (", baseFields, where)
	var batch []string
	flush := func() {
		if len(batch) > 0 {
			out = append(out, prefix+strings.Join(batch, ", ")+")")
			batch = nil
		}
	}
	for _, nt := range plain {
		quoted := "'" + nt + "'"
		if len(prefix)+len(strings.Join(append(batch, quoted), ", "))+1 > maxExpression {
			flush()
		}
		batch = append(batch, quoted)
	}
	flush()
	sort.Strings(out)
	return out
}

// configRow is one advanced-query result row.
type configRow struct {
	ResourceID           string `json:"resourceId"`
	ResourceName         string `json:"resourceName"`
	ResourceType         string `json:"resourceType"`
	AWSRegion            string `json:"awsRegion"`
	AccountID            string `json:"accountId"`
	ARN                  string `json:"arn"`
	ResourceCreationTime string `json:"resourceCreationTime"`
	Tags                 []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	} `json:"tags"`
	Configuration map[string]any `json:"configuration"`
}

// parseRows turns advanced-query rows into resources.
func parseRows(rows []string) ([]model.Resource, error) {
	out := make([]model.Resource, 0, len(rows))
	for _, raw := range rows {
		var r configRow
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			return nil, fmt.Errorf("AWS Config row: %w", err)
		}
		tags := make([]model.Tag, 0, len(r.Tags))
		for _, t := range r.Tags {
			tags = append(tags, model.Tag{Key: t.Key, Value: t.Value, Kind: model.KindTag})
		}
		res := resource(listRequest{Account: r.AccountID, Region: r.AWSRegion}, r.ResourceType,
			firstNonEmpty(r.ARN, r.ResourceType+"/"+r.ResourceID),
			firstNonEmpty(r.ResourceName, tagValue(tags, "Name"), r.ResourceID),
			parseTime(r.ResourceCreationTime), tags)
		res.Attrs = rowAttrs(r)
		out = append(out, res)
	}
	return out, nil
}

// rowAttrs sets the catalog discriminators from a row.
func rowAttrs(r configRow) map[string]string {
	attrs := map[string]string{}
	str := func(prop string) string {
		v, _ := r.Configuration[prop].(string)
		return v
	}
	switch r.ResourceType {
	case "AWS::RDS::DBInstance":
		attrs["engine"] = str("engine")
		attrs["cluster_member"] = fmt.Sprint(str("dBClusterIdentifier") != "")
	case "AWS::RDS::DBCluster":
		attrs["engine"] = str("engine")
	case "AWS::ElasticLoadBalancingV2::LoadBalancer":
		attrs["type"] = str("type")
	case "AWS::IAM::Policy":
		// AWS managed policies live in the "aws" account: arn:aws:iam::aws:policy/...
		attrs["policy_owner"] = "customer"
		if strings.Contains(r.ARN, ":iam::aws:policy/") {
			attrs["policy_owner"] = "aws"
		}
	}
	return attrs
}

// resource builds a normalized AWS resource with sorted tags.
func resource(req listRequest, native, id, name string, created *time.Time, tags []model.Tag) model.Resource {
	if tags == nil {
		tags = []model.Tag{}
	}
	sort.SliceStable(tags, func(i, j int) bool { return tags[i].Key < tags[j].Key })
	return model.Resource{
		Cloud: model.AWS, Scope: req.Account, Region: req.Region, NativeType: native,
		ID: id, Name: name, Created: created, Tags: tags, Parent: req.Account,
	}
}

func tagValue(tags []model.Tag, key string) string {
	for _, t := range tags {
		if t.Key == key {
			return t.Value
		}
	}
	return ""
}

func parseTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return nil
	}
	t = t.UTC()
	return &t
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

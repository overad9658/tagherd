package aws

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/overad9658/tagherd/internal/config"
	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/provider"
)

func (s *stub) WriteCaller(_ context.Context, account string) (string, error) {
	if err := s.writeErr[account]; err != nil {
		return "", err
	}
	return "arn:aws:sts::" + account + ":assumed-role/tagherd-write/tagherd-write", nil
}

func (s *stub) TaggedResources(_ context.Context, account, region string, arns []string) (map[string][]model.Tag, error) {
	s.record(fmt.Sprintf("read %s %s %d", account, region, len(arns)))
	out := map[string][]model.Tag{}
	for _, a := range arns {
		if t, ok := s.tagged[a]; ok {
			out[a] = t
		}
	}
	return out, nil
}

func (s *stub) TagResources(_ context.Context, account, region string, arns []string, tags map[string]string) (map[string]error, error) {
	s.record(fmt.Sprintf("tag %s %s %d %s", account, region, len(arns), tagKey(tags)))
	s.mu.Lock()
	defer s.mu.Unlock()
	failed := map[string]error{}
	for _, a := range arns {
		if err := s.failARN[a]; err != nil {
			failed[a] = err
			continue
		}
		s.write(a, tags)
	}
	return failed, nil
}

func (s *stub) ServiceTags(_ context.Context, account, service, id string) ([]model.Tag, error) {
	s.record(fmt.Sprintf("svc-read %s %s", service, account))
	return s.svcTags[id], nil
}

func (s *stub) TagService(_ context.Context, account, service, id string, tags map[string]string) error {
	s.record(fmt.Sprintf("svc-tag %s %s", service, account))
	s.mu.Lock()
	defer s.mu.Unlock()
	s.write(id, tags)
	return nil
}

func (s *stub) write(id string, tags map[string]string) {
	if s.written == nil {
		s.written = map[string]map[string]string{}
	}
	s.written[id] = tags
}

func TestWriter(t *testing.T) {
	b := &stub{
		mgmt:    "999",
		tagged:  map[string][]model.Tag{"arn:aws:s3:::b1": {{Key: "Environment", Value: "Production", Kind: model.KindTag}}},
		failARN: map[string]error{"arn:aws:s3:::b3": errors.New("AccessDeniedException: no")},
		svcTags: map[string][]model.Tag{"arn:aws:iam::111:role/app/r1": {{Key: "Brand", Value: "Acme"}}},
	}
	p := newProvider(Options{Scopes: config.AWSScopes{WriteRoleName: "tagherd-write"}}, b)
	ctx := context.Background()
	w, err := p.Writer(ctx, provider.Scope{Cloud: model.AWS, ID: "111"})
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := w.Actor(ctx); !strings.Contains(a, "assumed-role/tagherd-write") {
		t.Errorf("actor = %s", a)
	}

	b1 := provider.Target{ID: "arn:aws:s3:::b1", NativeType: "AWS::S3::Bucket", Region: "us-east-1"}
	b2 := provider.Target{ID: "arn:aws:s3:::b2", NativeType: "AWS::S3::Bucket", Region: "us-east-1"}
	b3 := provider.Target{ID: "arn:aws:s3:::b3", NativeType: "AWS::S3::Bucket", Region: "us-east-1"}
	role := provider.Target{ID: "arn:aws:iam::111:role/app/r1", NativeType: "AWS::IAM::Role"}
	acct := provider.Target{ID: "arn:aws:organizations::999:account/o-1/111", NativeType: "AWS::Organizations::Account"}
	noARN := provider.Target{ID: "AWS::X::Y/abc", NativeType: "AWS::X::Y"}

	tags, errs := w.Read(ctx, []provider.Target{b1, b2, role, acct, noARN})
	if len(tags["arn:aws:s3:::b1"]) != 1 || tags["arn:aws:s3:::b2"] == nil || len(tags[role.ID]) != 1 {
		t.Errorf("tags = %v", tags)
	}
	if len(errs) != 1 || errs[noARN.ID] == nil {
		t.Errorf("errs = %v", errs)
	}

	manual := []model.Tag{{Key: "DeploymentType", Value: "Manual"}}
	var writes []provider.TagWrite
	for i := range 21 {
		writes = append(writes, provider.TagWrite{Target: provider.Target{ID: fmt.Sprintf("arn:aws:s3:::x%d", i), Region: "us-east-1"}, Tags: manual})
	}
	writes = append(writes,
		provider.TagWrite{Target: b3, Tags: manual},
		provider.TagWrite{Target: b2, Tags: []model.Tag{{Key: "Brand", Value: "Acme"}}},
		provider.TagWrite{Target: role, Tags: manual},
		provider.TagWrite{Target: acct, Tags: manual},
		provider.TagWrite{Target: b1, Tags: []model.Tag{{Key: "aws:owner", Value: "x"}}},
	)
	b.calls = nil
	errs = w.Write(ctx, writes)
	if len(errs) != 2 || !strings.Contains(errs[b3.ID].Error(), "AccessDenied") || !strings.Contains(errs[b1.ID].Error(), "reserved") {
		t.Errorf("errs = %v", errs)
	}
	// 22 resources share the same tags: batches of 20 and 2; Brand is its
	// own call; IAM in the account; the account from the management one.
	want := []string{
		"svc-tag iam 111",
		"svc-tag organizations 999",
		"tag 111 us-east-1 20 DeploymentType\x00Manual\x00",
		"tag 111 us-east-1 2 DeploymentType\x00Manual\x00",
		"tag 111 us-east-1 1 Brand\x00Acme\x00",
	}
	if !slices.Equal(b.calls, want) {
		t.Errorf("calls = %q", b.calls)
	}
}

func TestWriterRoleFails(t *testing.T) {
	b := &stub{writeErr: map[string]error{"222": errors.New("AccessDenied")}}
	p := newProvider(Options{Scopes: config.AWSScopes{WriteRoleName: "tagherd-write"}}, b)
	if _, err := p.Writer(context.Background(), provider.Scope{ID: "222"}); err == nil || !strings.Contains(err.Error(), "assuming tagherd-write in 222") {
		t.Errorf("err = %v", err)
	}
	p = newProvider(Options{}, &stub{})
	w, _ := p.Writer(context.Background(), provider.Scope{ID: "111"})
	errs := w.Write(context.Background(), []provider.TagWrite{{Target: provider.Target{ID: "arn:aws:organizations::9:account/o/1", NativeType: "AWS::Organizations::Account"}}})
	if len(errs) != 1 {
		t.Errorf("no management account: %v", errs)
	}
}

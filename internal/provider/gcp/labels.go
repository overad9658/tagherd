package gcp

import (
	"context"
	"fmt"
	"strings"

	bigquery "google.golang.org/api/bigquery/v2"
	bigtable "google.golang.org/api/bigtableadmin/v2"
	functions1 "google.golang.org/api/cloudfunctions/v1"
	functions2 "google.golang.org/api/cloudfunctions/v2"
	compute "google.golang.org/api/compute/v1"
	file "google.golang.org/api/file/v1"
	memcache "google.golang.org/api/memcache/v1"
	"google.golang.org/api/option"
	redis "google.golang.org/api/redis/v1"
	spanner "google.golang.org/api/spanner/v1"
	sqladmin "google.golang.org/api/sqladmin/v1"
	storage "google.golang.org/api/storage/v1"
)

// labelAPI reads and writes one asset type's labels. path is the full
// resource name without "//<service>/". get returns a token (fingerprint
// or settings version) that set must send back, for types that need one.
type labelAPI struct {
	get func(ctx context.Context, path string) (map[string]string, string, error)
	set func(ctx context.Context, path string, labels map[string]string, token string) error
}

// resourcePath strips the service from a CAI full resource name:
// //compute.googleapis.com/projects/p/zones/z/instances/i becomes
// projects/p/zones/z/instances/i.
func resourcePath(id string) (string, error) {
	rest, ok := strings.CutPrefix(id, "//")
	if !ok {
		return "", fmt.Errorf("%s is not a full resource name", id)
	}
	_, path, ok := strings.Cut(rest, "/")
	if !ok {
		return "", fmt.Errorf("%s is not a full resource name", id)
	}
	return path, nil
}

// segments maps a path like projects/p/zones/z/instances/i to
// {projects: p, zones: z, instances: i}.
func segments(path string) map[string]string {
	parts := strings.Split(path, "/")
	out := map[string]string{}
	for i := 0; i+1 < len(parts); i += 2 {
		out[parts[i]] = parts[i+1]
	}
	return out
}

// labelAPIs builds the label calls for every GCP type tagherd writes: the
// Compute, Database and Storage groups.
func labelAPIs(ctx context.Context, opts []option.ClientOption) (map[string]labelAPI, error) {
	c, err := compute.NewService(ctx, opts...)
	if err != nil {
		return nil, err
	}
	st, err := storage.NewService(ctx, opts...)
	if err != nil {
		return nil, err
	}
	sql, err := sqladmin.NewService(ctx, opts...)
	if err != nil {
		return nil, err
	}
	bq, err := bigquery.NewService(ctx, opts...)
	if err != nil {
		return nil, err
	}
	bt, err := bigtable.NewService(ctx, opts...)
	if err != nil {
		return nil, err
	}
	rd, err := redis.NewService(ctx, opts...)
	if err != nil {
		return nil, err
	}
	mc, err := memcache.NewService(ctx, opts...)
	if err != nil {
		return nil, err
	}
	sp, err := spanner.NewService(ctx, opts...)
	if err != nil {
		return nil, err
	}
	fs, err := file.NewService(ctx, opts...)
	if err != nil {
		return nil, err
	}
	f1, err := functions1.NewService(ctx, opts...)
	if err != nil {
		return nil, err
	}
	f2, err := functions2.NewService(ctx, opts...)
	if err != nil {
		return nil, err
	}

	return map[string]labelAPI{
		// Compute label writes are asynchronous operations; --verify reads
		// the result back.
		"compute.googleapis.com/Instance": {
			get: func(ctx context.Context, path string) (map[string]string, string, error) {
				s := segments(path)
				i, err := c.Instances.Get(s["projects"], s["zones"], s["instances"]).Context(ctx).Do()
				if err != nil {
					return nil, "", err
				}
				return i.Labels, i.LabelFingerprint, nil
			},
			set: func(ctx context.Context, path string, labels map[string]string, fp string) error {
				s := segments(path)
				_, err := c.Instances.SetLabels(s["projects"], s["zones"], s["instances"],
					&compute.InstancesSetLabelsRequest{Labels: labels, LabelFingerprint: fp}).Context(ctx).Do()
				return err
			},
		},
		"compute.googleapis.com/Disk": {
			get: func(ctx context.Context, path string) (map[string]string, string, error) {
				s := segments(path)
				d, err := c.Disks.Get(s["projects"], s["zones"], s["disks"]).Context(ctx).Do()
				if err != nil {
					return nil, "", err
				}
				return d.Labels, d.LabelFingerprint, nil
			},
			set: func(ctx context.Context, path string, labels map[string]string, fp string) error {
				s := segments(path)
				_, err := c.Disks.SetLabels(s["projects"], s["zones"], s["disks"],
					&compute.ZoneSetLabelsRequest{Labels: labels, LabelFingerprint: fp}).Context(ctx).Do()
				return err
			},
		},
		"compute.googleapis.com/RegionDisk": {
			get: func(ctx context.Context, path string) (map[string]string, string, error) {
				s := segments(path)
				d, err := c.RegionDisks.Get(s["projects"], s["regions"], s["disks"]).Context(ctx).Do()
				if err != nil {
					return nil, "", err
				}
				return d.Labels, d.LabelFingerprint, nil
			},
			set: func(ctx context.Context, path string, labels map[string]string, fp string) error {
				s := segments(path)
				_, err := c.RegionDisks.SetLabels(s["projects"], s["regions"], s["disks"],
					&compute.RegionSetLabelsRequest{Labels: labels, LabelFingerprint: fp}).Context(ctx).Do()
				return err
			},
		},
		"compute.googleapis.com/Image": {
			get: func(ctx context.Context, path string) (map[string]string, string, error) {
				s := segments(path)
				i, err := c.Images.Get(s["projects"], s["images"]).Context(ctx).Do()
				if err != nil {
					return nil, "", err
				}
				return i.Labels, i.LabelFingerprint, nil
			},
			set: func(ctx context.Context, path string, labels map[string]string, fp string) error {
				s := segments(path)
				_, err := c.Images.SetLabels(s["projects"], s["images"],
					&compute.GlobalSetLabelsRequest{Labels: labels, LabelFingerprint: fp}).Context(ctx).Do()
				return err
			},
		},
		"compute.googleapis.com/Snapshot": {
			get: func(ctx context.Context, path string) (map[string]string, string, error) {
				s := segments(path)
				sn, err := c.Snapshots.Get(s["projects"], s["snapshots"]).Context(ctx).Do()
				if err != nil {
					return nil, "", err
				}
				return sn.Labels, sn.LabelFingerprint, nil
			},
			set: func(ctx context.Context, path string, labels map[string]string, fp string) error {
				s := segments(path)
				_, err := c.Snapshots.SetLabels(s["projects"], s["snapshots"],
					&compute.GlobalSetLabelsRequest{Labels: labels, LabelFingerprint: fp}).Context(ctx).Do()
				return err
			},
		},
		"storage.googleapis.com/Bucket": {
			get: func(ctx context.Context, path string) (map[string]string, string, error) {
				b, err := st.Buckets.Get(segments(path)["buckets"]).Context(ctx).Do()
				if err != nil {
					return nil, "", err
				}
				return b.Labels, "", nil
			},
			set: func(ctx context.Context, path string, labels map[string]string, _ string) error {
				_, err := st.Buckets.Patch(segments(path)["buckets"], &storage.Bucket{Labels: labels}).Context(ctx).Do()
				return err
			},
		},
		"sqladmin.googleapis.com/Instance": {
			get: func(ctx context.Context, path string) (map[string]string, string, error) {
				s := segments(path)
				i, err := sql.Instances.Get(s["projects"], s["instances"]).Context(ctx).Do()
				if err != nil {
					return nil, "", err
				}
				if i.Settings == nil {
					return nil, "", nil
				}
				return i.Settings.UserLabels, fmt.Sprint(i.Settings.SettingsVersion), nil
			},
			set: func(ctx context.Context, path string, labels map[string]string, version string) error {
				s := segments(path)
				var v int64
				_, _ = fmt.Sscan(version, &v)
				_, err := sql.Instances.Patch(s["projects"], s["instances"], &sqladmin.DatabaseInstance{
					Settings: &sqladmin.Settings{UserLabels: labels, SettingsVersion: v},
				}).Context(ctx).Do()
				return err
			},
		},
		"bigquery.googleapis.com/Dataset": {
			get: func(ctx context.Context, path string) (map[string]string, string, error) {
				s := segments(path)
				d, err := bq.Datasets.Get(s["projects"], s["datasets"]).Context(ctx).Do()
				if err != nil {
					return nil, "", err
				}
				return d.Labels, d.Etag, nil
			},
			set: func(ctx context.Context, path string, labels map[string]string, etag string) error {
				s := segments(path)
				call := bq.Datasets.Patch(s["projects"], s["datasets"], &bigquery.Dataset{Labels: labels}).Context(ctx)
				if etag != "" {
					call.Header().Set("If-Match", etag)
				}
				_, err := call.Do()
				return err
			},
		},
		"bigtableadmin.googleapis.com/Instance": {
			get: func(ctx context.Context, path string) (map[string]string, string, error) {
				i, err := bt.Projects.Instances.Get(path).Context(ctx).Do()
				if err != nil {
					return nil, "", err
				}
				return i.Labels, "", nil
			},
			set: func(ctx context.Context, path string, labels map[string]string, _ string) error {
				_, err := bt.Projects.Instances.PartialUpdateInstance(path, &bigtable.Instance{Labels: labels}).
					UpdateMask("labels").Context(ctx).Do()
				return err
			},
		},
		"redis.googleapis.com/Instance": {
			get: func(ctx context.Context, path string) (map[string]string, string, error) {
				i, err := rd.Projects.Locations.Instances.Get(path).Context(ctx).Do()
				if err != nil {
					return nil, "", err
				}
				return i.Labels, "", nil
			},
			set: func(ctx context.Context, path string, labels map[string]string, _ string) error {
				_, err := rd.Projects.Locations.Instances.Patch(path, &redis.Instance{Labels: labels}).
					UpdateMask("labels").Context(ctx).Do()
				return err
			},
		},
		"memcache.googleapis.com/Instance": {
			get: func(ctx context.Context, path string) (map[string]string, string, error) {
				i, err := mc.Projects.Locations.Instances.Get(path).Context(ctx).Do()
				if err != nil {
					return nil, "", err
				}
				return i.Labels, "", nil
			},
			set: func(ctx context.Context, path string, labels map[string]string, _ string) error {
				_, err := mc.Projects.Locations.Instances.Patch(path, &memcache.Instance{Labels: labels}).
					UpdateMask("labels").Context(ctx).Do()
				return err
			},
		},
		"spanner.googleapis.com/Instance": {
			get: func(ctx context.Context, path string) (map[string]string, string, error) {
				i, err := sp.Projects.Instances.Get(path).Context(ctx).Do()
				if err != nil {
					return nil, "", err
				}
				return i.Labels, "", nil
			},
			set: func(ctx context.Context, path string, labels map[string]string, _ string) error {
				_, err := sp.Projects.Instances.Patch(path, &spanner.UpdateInstanceRequest{
					Instance: &spanner.Instance{Labels: labels}, FieldMask: "labels",
				}).Context(ctx).Do()
				return err
			},
		},
		"file.googleapis.com/Instance": {
			get: func(ctx context.Context, path string) (map[string]string, string, error) {
				i, err := fs.Projects.Locations.Instances.Get(path).Context(ctx).Do()
				if err != nil {
					return nil, "", err
				}
				return i.Labels, "", nil
			},
			set: func(ctx context.Context, path string, labels map[string]string, _ string) error {
				_, err := fs.Projects.Locations.Instances.Patch(path, &file.Instance{Labels: labels}).
					UpdateMask("labels").Context(ctx).Do()
				return err
			},
		},
		"cloudfunctions.googleapis.com/CloudFunction": { // 1st gen
			get: func(ctx context.Context, path string) (map[string]string, string, error) {
				f, err := f1.Projects.Locations.Functions.Get(path).Context(ctx).Do()
				if err != nil {
					return nil, "", err
				}
				return f.Labels, "", nil
			},
			set: func(ctx context.Context, path string, labels map[string]string, _ string) error {
				_, err := f1.Projects.Locations.Functions.Patch(path, &functions1.CloudFunction{Labels: labels}).
					UpdateMask("labels").Context(ctx).Do()
				return err
			},
		},
		"cloudfunctions.googleapis.com/Function": { // 2nd gen
			get: func(ctx context.Context, path string) (map[string]string, string, error) {
				f, err := f2.Projects.Locations.Functions.Get(path).Context(ctx).Do()
				if err != nil {
					return nil, "", err
				}
				return f.Labels, "", nil
			},
			set: func(ctx context.Context, path string, labels map[string]string, _ string) error {
				_, err := f2.Projects.Locations.Functions.Patch(path, &functions2.Function{Labels: labels}).
					UpdateMask("labels").Context(ctx).Do()
				return err
			},
		},
	}, nil
}

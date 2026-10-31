// Package model holds the plain data types shared by every tagherd component.
// Their JSON shape is the public contract for output, plan files and the API.
package model

import (
	"fmt"
	"strings"
)

// Cloud identifies a cloud provider. Values match the keys used in policy files.
type Cloud string

const (
	AWS   Cloud = "AWS"
	Azure Cloud = "Azure"
	GCP   Cloud = "GCP"
	OCI   Cloud = "OCI"
)

// Clouds lists every supported cloud in display order.
var Clouds = []Cloud{AWS, Azure, GCP, OCI}

// ParseCloud accepts a cloud name in any case ("aws", "Azure", "GCP", "oci").
func ParseCloud(s string) (Cloud, error) {
	for _, c := range Clouds {
		if strings.EqualFold(s, string(c)) {
			return c, nil
		}
	}
	return "", fmt.Errorf("unknown cloud %q (want aws, azure, gcp or oci)", s)
}

// Prefix is the word every display name for this cloud starts with, plus a space.
func (c Cloud) Prefix() string { return string(c) + " " }

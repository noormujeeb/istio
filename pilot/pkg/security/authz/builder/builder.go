// Copyright 2020 Istio Authors
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

package builder

import (
	"fmt"

	"istio.io/istio/pilot/pkg/model"
	"istio.io/istio/pilot/pkg/networking/util"
	authzmodel "istio.io/istio/pilot/pkg/security/authz/model"
	"istio.io/istio/pilot/pkg/security/trustdomain"
	"istio.io/istio/pkg/config/labels"
	"istio.io/pkg/log"

	tcppb "github.com/envoyproxy/go-control-plane/envoy/api/v2/listener"
	rbachttppb "github.com/envoyproxy/go-control-plane/envoy/config/filter/http/rbac/v2"
	httppb "github.com/envoyproxy/go-control-plane/envoy/config/filter/network/http_connection_manager/v2"
	rbactcppb "github.com/envoyproxy/go-control-plane/envoy/config/filter/network/rbac/v2"
	rbacpb "github.com/envoyproxy/go-control-plane/envoy/config/rbac/v2"
)

var (
	authzLog = log.RegisterScope("authorization", "Istio Authorization Policy", 0)
)

// Builder builds Istio authorization policy to Envoy RBAC filter.
type Builder struct {
	trustDomainBundle trustdomain.Bundle
	denyPolicies      []model.AuthorizationPolicyConfig
	allowPolicies     []model.AuthorizationPolicyConfig
}

// New returns a new builder for the given workload with the authorization policy.
// Returns nil if none of the authorization policies are enabled for the workload.
func New(trustDomainBundle trustdomain.Bundle, workload labels.Collection, namespace string,
	policies *model.AuthorizationPolicies) *Builder {
	denyPolicies, allowPolicies := policies.ListAuthorizationPolicies(namespace, workload)
	if len(denyPolicies) == 0 && len(allowPolicies) == 0 {
		return nil
	}
	return &Builder{
		trustDomainBundle: trustDomainBundle,
		denyPolicies:      denyPolicies,
		allowPolicies:     allowPolicies,
	}
}

// BuilderHTTP returns the RBAC HTTP filters built from the authorization policy.
func (b Builder) BuildHTTP() []*httppb.HttpFilter {
	var filters []*httppb.HttpFilter

	if denyConfig := build(b.denyPolicies, b.trustDomainBundle,
		false /* forTCP */, true /* forDeny */); denyConfig != nil {
		filters = append(filters, createHTTPFilter(denyConfig))
	}
	if allowConfig := build(b.allowPolicies, b.trustDomainBundle,
		false /* forTCP */, false /* forDeny */); allowConfig != nil {
		filters = append(filters, createHTTPFilter(allowConfig))
	}

	return filters
}

// BuildTCP returns the RBAC TCP filters built from the authorization policy.
func (b Builder) BuildTCP() []*tcppb.Filter {
	var filters []*tcppb.Filter

	if denyConfig := build(b.denyPolicies, b.trustDomainBundle,
		true /* forTCP */, true /* forDeny */); denyConfig != nil {
		filters = append(filters, createTCPFilter(denyConfig))
	}
	if allowConfig := build(b.allowPolicies, b.trustDomainBundle,
		true /* forTCP */, false /* forDeny */); allowConfig != nil {
		filters = append(filters, createTCPFilter(allowConfig))
	}

	return filters
}

func build(policies []model.AuthorizationPolicyConfig, tdBundle trustdomain.Bundle, forTCP, forDeny bool) *rbachttppb.RBAC {
	if len(policies) == 0 {
		return nil
	}

	rules := &rbacpb.RBAC{
		Action:   rbacpb.RBAC_ALLOW,
		Policies: map[string]*rbacpb.Policy{},
	}
	if forDeny {
		rules.Action = rbacpb.RBAC_DENY
	}
	for _, policy := range policies {
		for i, rule := range policy.AuthorizationPolicy.Rules {
			name := fmt.Sprintf("ns[%s]-policy[%s]-rule[%d]", policy.Namespace, policy.Name, i)
			if rule == nil {
				authzLog.Errorf("skipped nil rule %s", name)
				continue
			}
			m, err := authzmodel.New(rule)
			if err != nil {
				authzLog.Errorf("skipped rule %s: %v", name, err)
				continue
			}
			m.MigrateTrustDomain(tdBundle)
			generated, err := m.Generate(forTCP, forDeny)
			if err != nil {
				authzLog.Errorf("skipped rule %s: %v", name, err)
				continue
			}
			if generated != nil {
				rules.Policies[name] = generated
				authzLog.Debugf("rule %s generated policy: %+v", name, generated)
			}
		}
	}

	return &rbachttppb.RBAC{Rules: rules}
}

// nolint: interfacer
func createHTTPFilter(config *rbachttppb.RBAC) *httppb.HttpFilter {
	if config == nil {
		return nil
	}
	return &httppb.HttpFilter{
		Name:       authzmodel.RBACHTTPFilterName,
		ConfigType: &httppb.HttpFilter_TypedConfig{TypedConfig: util.MessageToAny(config)},
	}
}

func createTCPFilter(config *rbachttppb.RBAC) *tcppb.Filter {
	if config == nil {
		return nil
	}
	rbacConfig := &rbactcppb.RBAC{
		Rules:      config.Rules,
		StatPrefix: authzmodel.RBACTCPFilterStatPrefix,
	}
	return &tcppb.Filter{
		Name:       authzmodel.RBACTCPFilterName,
		ConfigType: &tcppb.Filter_TypedConfig{TypedConfig: util.MessageToAny(rbacConfig)},
	}
}

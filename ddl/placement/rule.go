// Copyright 2021 PingCAP, Inc.
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

package placement

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/pingcap/tidb/util/codec"
	"gopkg.in/yaml.v2"
)

// PeerRoleType is the expected peer type of the placement rule.
type PeerRoleType string

const (
	// Voter can either match a leader peer or follower peer.
	Voter PeerRoleType = "voter"
	// Leader matches a leader.
	Leader PeerRoleType = "leader"
	// Follower matches a follower.
	Follower PeerRoleType = "follower"
	// Learner matches a learner.
	Learner PeerRoleType = "learner"
)

const (
	attributePrefix = "#"
	// AttributeEvictLeader is used to evict leader from a store.
	attributeEvictLeader = "evict-leader"
)

// RuleGroupConfig defines basic config of rule group
type RuleGroupConfig struct {
	ID       string `json:"id"`
	Index    int    `json:"index"`
	Override bool   `json:"override"`
}

// Rule is the core placement rule struct. Check https://github.com/tikv/pd/blob/master/server/schedule/placement/rule.go.
type Rule struct {
	GroupID        string       `json:"group_id"`
	ID             string       `json:"id"`
	Index          int          `json:"index,omitempty"`
	Override       bool         `json:"override,omitempty"`
	StartKeyHex    string       `json:"start_key"`
	EndKeyHex      string       `json:"end_key"`
	Role           PeerRoleType `json:"role"`
	Count          int          `json:"count"`
	Constraints    Constraints  `json:"label_constraints,omitempty"`
	LocationLabels []string     `json:"location_labels,omitempty"`
}

var _ json.Marshaler = (*TiFlashRule)(nil)
var _ json.Unmarshaler = (*TiFlashRule)(nil)

// TiFlashRule extends Rule with other necessary fields.
type TiFlashRule struct {
	GroupID        string
	ID             string
	Index          int
	Override       bool
	Role           PeerRoleType
	Count          int
	Constraints    Constraints
	LocationLabels []string
	IsolationLevel string
	StartKey       []byte
	EndKey         []byte
}

type tiFlashRule struct {
	GroupID        string       `json:"group_id"`
	ID             string       `json:"id"`
	Index          int          `json:"index,omitempty"`
	Override       bool         `json:"override,omitempty"`
	Role           PeerRoleType `json:"role"`
	Count          int          `json:"count"`
	Constraints    Constraints  `json:"label_constraints,omitempty"`
	LocationLabels []string     `json:"location_labels,omitempty"`
	IsolationLevel string       `json:"isolation_level,omitempty"`
	StartKeyHex    string       `json:"start_key"`
	EndKeyHex      string       `json:"end_key"`
}

// MarshalJSON implements json.Marshaler interface for TiFlashRule.
func (r *TiFlashRule) MarshalJSON() ([]byte, error) {
	return json.Marshal(&tiFlashRule{
		GroupID:        r.GroupID,
		ID:             r.ID,
		Index:          r.Index,
		Override:       r.Override,
		Role:           r.Role,
		Count:          r.Count,
		Constraints:    r.Constraints,
		LocationLabels: r.LocationLabels,
		IsolationLevel: r.IsolationLevel,
		StartKeyHex:    hex.EncodeToString(codec.EncodeBytes(nil, r.StartKey)),
		EndKeyHex:      hex.EncodeToString(codec.EncodeBytes(nil, r.EndKey)),
	})
}

// UnmarshalJSON implements json.Unmarshaler interface for TiFlashRule.
func (r *TiFlashRule) UnmarshalJSON(bytes []byte) error {
	var rule tiFlashRule
	if err := json.Unmarshal(bytes, &rule); err != nil {
		return err
	}
	*r = TiFlashRule{
		GroupID:        rule.GroupID,
		ID:             rule.ID,
		Index:          rule.Index,
		Override:       rule.Override,
		Role:           rule.Role,
		Count:          rule.Count,
		Constraints:    rule.Constraints,
		LocationLabels: rule.LocationLabels,
		IsolationLevel: rule.IsolationLevel,
	}

	startKey, err := hex.DecodeString(rule.StartKeyHex)
	if err != nil {
		return err
	}

	endKey, err := hex.DecodeString(rule.EndKeyHex)
	if err != nil {
		return err
	}

	_, r.StartKey, err = codec.DecodeBytes(startKey, nil)
	if err != nil {
		return err
	}

	_, r.EndKey, err = codec.DecodeBytes(endKey, nil)

	return err
}

// NewRule constructs *Rule from role, count, and constraints. It is here to
// consistent the behavior of creating new rules.
func NewRule(role PeerRoleType, replicas uint64, cnst Constraints) *Rule {
	return &Rule{
		Role:        role,
		Count:       int(replicas),
		Constraints: cnst,
	}
}

var wrongSeparatorRegexp = regexp.MustCompile(`[^"':]+:\d`)

func getYamlMapFormatError(str string) error {
	if !strings.Contains(str, ":") {
		return ErrInvalidConstraintsMappingNoColonFound
	}
	if wrongSeparatorRegexp.MatchString(str) {
		return ErrInvalidConstraintsMappingWrongSeparator
	}
	return nil
}

// NewRules constructs []*Rule from a yaml-compatible representation of
// 'array' or 'dict' constraints.
// Refer to https://github.com/pingcap/tidb/blob/master/docs/design/2020-06-24-placement-rules-in-sql.md.
func NewRules(role PeerRoleType, replicas uint64, cnstr string) (rules []*Rule, err error) {
	cnstbytes := []byte(cnstr)
	constraints1, err1 := NewConstraintsFromYaml(cnstbytes)
	if err1 == nil {
		if replicas == 0 {
			if len(cnstr) > 0 {
				return nil, fmt.Errorf("%w: count of replicas should be positive, but got %d, constraint %s", ErrInvalidConstraintsReplicas, replicas, cnstr)
			}
			return nil, nil
		}
		rules = append(rules, NewRule(role, replicas, constraints1))
		err = err1
		return
	}
	// check if is dict constraints
	constraints2 := map[string]int{}
	if err2 := yaml.UnmarshalStrict(cnstbytes, &constraints2); err2 != nil {
		err = fmt.Errorf("%w: should be [constraint1, ...] (error %s), {constraint1: cnt1, ...} (error %s), or any yaml compatible representation", ErrInvalidConstraintsFormat, err1, err2)
		return
	}

	rules, err = NewRulesWithDictConstraints(role, cnstr)
	// check if replicas is consistent
	if err == nil {
		totalCnt := 0
		for _, rule := range rules {
			totalCnt += rule.Count
		}
		if replicas != 0 && replicas != uint64(totalCnt) {
			err = fmt.Errorf("%w: count of replicas in dict constrains is %d, but got %d", ErrInvalidConstraintsReplicas, totalCnt, replicas)
		}
	}
	return
}

// NewRulesWithDictConstraints constructs []*Rule from a yaml-compatible representation of
// 'dict' constraints.
func NewRulesWithDictConstraints(role PeerRoleType, cnstr string) ([]*Rule, error) {
	rules := []*Rule{}
	cnstbytes := []byte(cnstr)
	constraints2 := map[string]int{}
	err2 := yaml.UnmarshalStrict(cnstbytes, &constraints2)
	if err2 == nil {
		for labels, cnt := range constraints2 {
			if cnt <= 0 {
				if err := getYamlMapFormatError(string(cnstbytes)); err != nil {
					return rules, err
				}
				return rules, fmt.Errorf("%w: count of labels '%s' should be positive, but got %d", ErrInvalidConstraintsMapcnt, labels, cnt)
			}
		}

		for labels, cnt := range constraints2 {
			lbs, overrideRole, err := preCheckDictConstraintStr(labels, role)
			if err != nil {
				return rules, err
			}
			labelConstraints, err := NewConstraints(lbs)
			if err != nil {
				return rules, err
			}
			if cnt == 0 {
				return nil, fmt.Errorf("%w: count of replicas should be positive, but got %d", ErrInvalidConstraintsReplicas, cnt)
			}
			rules = append(rules, NewRule(overrideRole, uint64(cnt), labelConstraints))
		}
		return rules, nil
	}

	return nil, fmt.Errorf("%w: should be [constraint1, ...] or {constraint1: cnt1, ...}, error %s, or any yaml compatible representation", ErrInvalidConstraintsFormat, err2)
}

// Clone is used to duplicate a RuleOp for safe modification.
// Note that it is a shallow copy: Constraints is not cloned.
func (r *Rule) Clone() *Rule {
	n := &Rule{}
	*n = *r
	return n
}

func (r *Rule) String() string {
	return fmt.Sprintf("%+v", *r)
}

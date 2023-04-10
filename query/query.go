// Copyright 2016 Google Inc. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package query

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"log"
	"reflect"
	"regexp/syntax"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/RoaringBitmap/roaring"
	"github.com/grafana/regexp"

	v1 "github.com/sourcegraph/zoekt/grpc/v1"
)

var _ = log.Println

// Q is a representation for a possibly hierarchical search query.
type Q interface {
	String() string
}

func QToProto(q Q) *v1.Q {
	switch v := q.(type) {
	case *RawConfig:
		return &v1.Q{Query: &v1.Q_RawConfig{RawConfig: v.ToProto()}}
	case *Regexp:
		return &v1.Q{Query: &v1.Q_Regexp{Regexp: v.ToProto()}}
	case *Symbol:
		return &v1.Q{Query: &v1.Q_Symbol{Symbol: v.ToProto()}}
	case *Language:
		return &v1.Q{Query: &v1.Q_Language{Language: v.ToProto()}}
	case *Const:
		return &v1.Q{Query: &v1.Q_Const{Const: v.ToProto()}}
	case *Repo:
		return &v1.Q{Query: &v1.Q_Repo{Repo: v.ToProto()}}
	case *RepoRegexp:
		return &v1.Q{Query: &v1.Q_RepoRegexp{RepoRegexp: v.ToProto()}}
	case *BranchesRepos:
		return &v1.Q{Query: &v1.Q_BranchesRepos{BranchesRepos: v.ToProto()}}
	case *RepoIDs:
		return &v1.Q{Query: &v1.Q_RepoIds{RepoIds: v.ToProto()}}
	case *RepoSet:
		return &v1.Q{Query: &v1.Q_RepoSet{RepoSet: v.ToProto()}}
	case *FileNameSet:
		return &v1.Q{Query: &v1.Q_FileNameSet{FileNameSet: v.ToProto()}}
	case *Type:
		return &v1.Q{Query: &v1.Q_Type{Type: v.ToProto()}}
	case *Substring:
		return &v1.Q{Query: &v1.Q_Substring{Substring: v.ToProto()}}
	case *And:
		return &v1.Q{Query: &v1.Q_And{And: v.ToProto()}}
	case *Or:
		return &v1.Q{Query: &v1.Q_Or{Or: v.ToProto()}}
	case *Not:
		return &v1.Q{Query: &v1.Q_Not{Not: v.ToProto()}}
	case *Branch:
		return &v1.Q{Query: &v1.Q_Branch{Branch: v.ToProto()}}
	default:
		panic(fmt.Sprintf("unknown query node %T", v))
	}
}

func QFromProto(p *v1.Q) (Q, error) {
	switch v := p.Query.(type) {
	case *v1.Q_RawConfig:
		return RawConfigFromProto(v.RawConfig), nil
	case *v1.Q_Regexp:
		return RegexpFromProto(v.Regexp)
	case *v1.Q_Symbol:
		return SymbolFromProto(v.Symbol)
	case *v1.Q_Language:
		return LanguageFromProto(v.Language), nil
	case *v1.Q_Const:
		return ConstFromProto(v.Const), nil
	case *v1.Q_Repo:
		return RepoFromProto(v.Repo)
	case *v1.Q_RepoRegexp:
		return RepoRegexpFromProto(v.RepoRegexp)
	case *v1.Q_BranchesRepos:
		return BranchesReposFromProto(v.BranchesRepos)
	case *v1.Q_RepoIds:
		return RepoIDsFromProto(v.RepoIds)
	case *v1.Q_RepoSet:
		return RepoSetFromProto(v.RepoSet), nil
	case *v1.Q_FileNameSet:
		return FileNameSetFromProto(v.FileNameSet), nil
	case *v1.Q_Type:
		return TypeFromProto(v.Type)
	case *v1.Q_Substring:
		return SubstringFromProto(v.Substring), nil
	case *v1.Q_And:
		return AndFromProto(v.And)
	case *v1.Q_Or:
		return OrFromProto(v.Or)
	case *v1.Q_Not:
		return NotFromProto(v.Not)
	case *v1.Q_Branch:
		return BranchFromProto(v.Branch), nil
	default:
		panic(fmt.Sprintf("unknown query node %T", p.Query))
	}
}

// RPCUnwrap processes q to remove RPC specific elements from q. This is
// needed because gob isn't flexible enough for us. This should be called by
// RPC servers at the client/server boundary so that q works with the rest of
// zoekt.
func RPCUnwrap(q Q) Q {
	if cache, ok := q.(*GobCache); ok {
		return cache.Q
	}
	return q
}

// RawConfig filters repositories based on their encoded RawConfig map.
type RawConfig uint64

const (
	RcOnlyPublic   RawConfig = 1
	RcOnlyPrivate  RawConfig = 2
	RcOnlyForks    RawConfig = 1 << 2
	RcNoForks      RawConfig = 2 << 2
	RcOnlyArchived RawConfig = 1 << 4
	RcNoArchived   RawConfig = 2 << 4
)

var flagNames = []struct {
	Mask  RawConfig
	Label string
}{
	{RcOnlyPublic, "RcOnlyPublic"},
	{RcOnlyPrivate, "RcOnlyPrivate"},
	{RcOnlyForks, "RcOnlyForks"},
	{RcNoForks, "RcNoForks"},
	{RcOnlyArchived, "RcOnlyArchived"},
	{RcNoArchived, "RcNoArchived"},
}

func RawConfigFromProto(p *v1.RawConfig) RawConfig {
	return RawConfig(p.Flags)
}

func (r RawConfig) ToProto() *v1.RawConfig {
	return &v1.RawConfig{Flags: uint64(r)}
}

func (r RawConfig) String() string {
	var s []string
	for _, fn := range flagNames {
		if r&fn.Mask != 0 {
			s = append(s, fn.Label)
		}
	}
	return fmt.Sprintf("rawConfig:%s", strings.Join(s, "|"))
}

// RegexpQuery is a query looking for regular expressions matches.
type Regexp struct {
	Regexp        *syntax.Regexp
	FileName      bool
	Content       bool
	CaseSensitive bool
}

func RegexpFromProto(p *v1.Regexp) (*Regexp, error) {
	parsed, err := syntax.Parse(p.GetRegexp(), regexpFlags)
	if err != nil {
		return nil, err
	}
	return &Regexp{
		Regexp:        parsed,
		FileName:      p.GetFileName(),
		Content:       p.GetContent(),
		CaseSensitive: p.GetCaseSensitive(),
	}, nil
}

func (r *Regexp) ToProto() *v1.Regexp {
	return &v1.Regexp{
		Regexp:        r.Regexp.String(),
		FileName:      r.FileName,
		Content:       r.Content,
		CaseSensitive: r.CaseSensitive,
	}
}

func (q *Regexp) String() string {
	pref := ""
	if q.FileName {
		pref = "file_"
	}
	if q.CaseSensitive {
		pref = "case_" + pref
	}
	return fmt.Sprintf("%sregex:%q", pref, q.Regexp.String())
}

// gobRegexp wraps Regexp to make it gob-encodable/decodable. Regexp contains syntax.Regexp, which
// contains slices/arrays with possibly nil elements, which gob doesn't support
// (https://github.com/golang/go/issues/1501).
type gobRegexp struct {
	Regexp       // Regexp.Regexp (*syntax.Regexp) is set to nil and its string is set in RegexpString
	RegexpString string
}

// GobEncode implements gob.Encoder.
func (q Regexp) GobEncode() ([]byte, error) {
	gobq := gobRegexp{Regexp: q, RegexpString: q.Regexp.String()}
	gobq.Regexp.Regexp = nil // can't be gob-encoded/decoded
	return json.Marshal(gobq)
}

// GobDecode implements gob.Decoder.
func (q *Regexp) GobDecode(data []byte) error {
	var gobq gobRegexp
	err := json.Unmarshal(data, &gobq)
	if err != nil {
		return err
	}
	gobq.Regexp.Regexp, err = syntax.Parse(gobq.RegexpString, regexpFlags)
	if err != nil {
		return err
	}
	*q = gobq.Regexp
	return nil
}

// Symbol finds a string that is a symbol.
type Symbol struct {
	Expr Q
}

func SymbolFromProto(p *v1.Symbol) (*Symbol, error) {
	expr, err := QFromProto(p.GetExpr())
	if err != nil {
		return nil, err
	}

	return &Symbol{
		Expr: expr,
	}, nil
}

func (s *Symbol) ToProto() *v1.Symbol {
	return &v1.Symbol{
		Expr: QToProto(s.Expr),
	}
}

func (s *Symbol) String() string {
	return fmt.Sprintf("sym:%s", s.Expr)
}

type caseQ struct {
	Flavor string
}

func (c *caseQ) String() string {
	return "case:" + c.Flavor
}

type Language struct {
	Language string
}

func LanguageFromProto(p *v1.Language) *Language {
	return &Language{
		Language: p.GetLanguage(),
	}
}

func (l *Language) ToProto() *v1.Language {
	return &v1.Language{Language: l.Language}
}

func (l *Language) String() string {
	return "lang:" + l.Language
}

type Const struct {
	Value bool
}

func ConstFromProto(p *v1.Const) *Const {
	return &Const{
		Value: p.GetValue(),
	}
}

func (q *Const) ToProto() *v1.Const {
	return &v1.Const{Value: q.Value}
}

func (q *Const) String() string {
	if q.Value {
		return "TRUE"
	}
	return "FALSE"
}

type Repo struct {
	Regexp *regexp.Regexp
}

func RepoFromProto(p *v1.Repo) (*Repo, error) {
	r, err := regexp.Compile(p.GetRegexp())
	if err != nil {
		return nil, err
	}
	return &Repo{
		Regexp: r,
	}, nil
}

func (q *Repo) ToProto() *v1.Repo {
	return &v1.Repo{
		Regexp: q.Regexp.String(),
	}
}

func (q *Repo) String() string {
	return fmt.Sprintf("repo:%s", q.Regexp.String())
}

func (q Repo) GobEncode() ([]byte, error) {
	return []byte(q.Regexp.String()), nil
}

func (q *Repo) GobDecode(data []byte) error {
	var err error
	q.Regexp, err = regexp.Compile(string(data))
	return err
}

// RepoRegexp is a Sourcegraph addition which searches documents where the
// repository name matches Regexp.
type RepoRegexp struct {
	Regexp *regexp.Regexp
}

func RepoRegexpFromProto(p *v1.RepoRegexp) (*RepoRegexp, error) {
	r, err := regexp.Compile(p.GetRegexp())
	if err != nil {
		return nil, err
	}
	return &RepoRegexp{
		Regexp: r,
	}, nil
}

func (q *RepoRegexp) ToProto() *v1.RepoRegexp {
	return &v1.RepoRegexp{
		Regexp: q.Regexp.String(),
	}
}

func (q *RepoRegexp) String() string {
	return fmt.Sprintf("reporegex:%q", q.Regexp.String())
}

// GobEncode implements gob.Encoder.
func (q *RepoRegexp) GobEncode() ([]byte, error) {
	// gob can't encode syntax.Regexp
	return []byte(q.Regexp.String()), nil
}

// GobDecode implements gob.Decoder.
func (q *RepoRegexp) GobDecode(data []byte) error {
	var err error
	q.Regexp, err = regexp.Compile(string(data))
	return err
}

// BranchesRepos is a slice of BranchRepos to match. It is a Sourcegraph
// addition and only used in the RPC interface for efficient checking of large
// repo lists.
type BranchesRepos struct {
	List []BranchRepos
}

func BranchesReposFromProto(p *v1.BranchesRepos) (*BranchesRepos, error) {
	brs := make([]BranchRepos, len(p.GetList()))
	for i, br := range p.GetList() {
		branchRepos, err := BranchReposFromProto(br)
		if err != nil {
			return nil, err
		}
		brs[i] = branchRepos
	}
	return &BranchesRepos{
		List: brs,
	}, nil
}

func (br *BranchesRepos) ToProto() *v1.BranchesRepos {
	list := make([]*v1.BranchRepos, len(br.List))
	for i, branchRepo := range br.List {
		list[i] = branchRepo.ToProto()
	}

	return &v1.BranchesRepos{
		List: list,
	}
}

// NewSingleBranchesRepos is a helper for creating a BranchesRepos which
// searches a single branch.
func NewSingleBranchesRepos(branch string, ids ...uint32) *BranchesRepos {
	return &BranchesRepos{List: []BranchRepos{
		{branch, roaring.BitmapOf(ids...)},
	}}
}

func (q *BranchesRepos) String() string {
	var sb strings.Builder

	sb.WriteString("(branchesrepos")

	for _, br := range q.List {
		if size := br.Repos.GetCardinality(); size > 1 {
			sb.WriteString(" " + br.Branch + ":" + strconv.FormatUint(size, 10))
		} else {
			sb.WriteString(" " + br.Branch + "=" + br.Repos.String())
		}
	}

	sb.WriteString(")")
	return sb.String()
}

// NewRepoIDs is a helper for creating a RepoIDs which
// searches only the matched repos.
func NewRepoIDs(ids ...uint32) *RepoIDs {
	return &RepoIDs{Repos: roaring.BitmapOf(ids...)}
}

func RepoIDsFromProto(p *v1.RepoIds) (*RepoIDs, error) {
	bm := roaring.NewBitmap()
	err := bm.UnmarshalBinary(p.GetRepos())
	if err != nil {
		return nil, err
	}

	return &RepoIDs{
		Repos: bm,
	}, nil
}

func (q *RepoIDs) ToProto() *v1.RepoIds {
	b, err := q.Repos.ToBytes()
	if err != nil {
		panic("unexpected error marshalling bitmap: " + err.Error())
	}
	return &v1.RepoIds{
		Repos: b,
	}
}

func (q *RepoIDs) String() string {
	var sb strings.Builder

	sb.WriteString("(repoids ")

	if size := q.Repos.GetCardinality(); size > 1 {
		sb.WriteString("count:" + strconv.FormatUint(size, 10))
	} else {
		sb.WriteString("repoid=" + q.Repos.String())
	}

	sb.WriteString(")")
	return sb.String()
}

// MarshalBinary implements a specialized encoder for BranchesRepos.
func (q BranchesRepos) MarshalBinary() ([]byte, error) {
	return branchesReposEncode(q.List)
}

// UnmarshalBinary implements a specialized decoder for BranchesRepos.
func (q *BranchesRepos) UnmarshalBinary(b []byte) (err error) {
	q.List, err = branchesReposDecode(b)
	return err
}

// BranchRepos is a (branch, sourcegraph repo ids bitmap) tuple. It is a
// Sourcegraph addition.
type BranchRepos struct {
	Branch string
	Repos  *roaring.Bitmap
}

func BranchReposFromProto(p *v1.BranchRepos) (BranchRepos, error) {
	bm := roaring.NewBitmap()
	err := bm.UnmarshalBinary(p.GetRepos())
	if err != nil {
		return BranchRepos{}, err
	}
	return BranchRepos{
		Branch: p.GetBranch(),
		Repos:  bm,
	}, nil
}

func (br *BranchRepos) ToProto() *v1.BranchRepos {
	b, err := br.Repos.ToBytes()
	if err != nil {
		panic("unexpected error marshalling bitmap: " + err.Error())
	}

	return &v1.BranchRepos{
		Branch: br.Branch,
		Repos:  b,
	}
}

// Similar to BranchRepos but will be used to match only by repoid and
// therefore matches all branches
type RepoIDs struct {
	Repos *roaring.Bitmap
}

// RepoSet is a list of repos to match. It is a Sourcegraph addition and only
// used in the RPC interface for efficient checking of large repo lists.
type RepoSet struct {
	Set map[string]bool
}

func RepoSetFromProto(p *v1.RepoSet) *RepoSet {
	return &RepoSet{
		Set: p.GetSet(),
	}
}

func (q *RepoSet) ToProto() *v1.RepoSet {
	return &v1.RepoSet{
		Set: q.Set,
	}
}

func (q *RepoSet) String() string {
	var detail string
	if len(q.Set) > 5 {
		// Large sets being output are not useful
		detail = fmt.Sprintf("size=%d", len(q.Set))
	} else {
		repos := make([]string, len(q.Set))
		i := 0
		for repo := range q.Set {
			repos[i] = repo
			i++
		}
		sort.Strings(repos)
		detail = strings.Join(repos, " ")
	}
	return fmt.Sprintf("(reposet %s)", detail)
}

func NewRepoSet(repo ...string) *RepoSet {
	s := &RepoSet{Set: make(map[string]bool)}
	for _, r := range repo {
		s.Set[r] = true
	}
	return s
}

// FileNameSet is a list of file names to match. It is a Sourcegraph addition
// and only used in the RPC interface for efficient checking of large file
// lists.
type FileNameSet struct {
	Set map[string]struct{}
}

func FileNameSetFromProto(p *v1.FileNameSet) *FileNameSet {
	m := make(map[string]struct{}, len(p.GetSet()))
	for _, name := range p.GetSet() {
		m[name] = struct{}{}
	}
	return &FileNameSet{
		Set: m,
	}
}

func (q *FileNameSet) ToProto() *v1.FileNameSet {
	s := make([]string, 0, len(q.Set))
	for name := range q.Set {
		s = append(s, name)
	}
	return &v1.FileNameSet{
		Set: s,
	}
}

// MarshalBinary implements a specialized encoder for FileNameSet.
func (q *FileNameSet) MarshalBinary() ([]byte, error) {
	return stringSetEncode(q.Set)
}

// UnmarshalBinary implements a specialized decoder for FileNameSet.
func (q *FileNameSet) UnmarshalBinary(b []byte) error {
	var err error
	q.Set, err = stringSetDecode(b)
	return err
}

func (q *FileNameSet) String() string {
	var detail string
	if len(q.Set) > 5 {
		// Large sets being output are not useful
		detail = fmt.Sprintf("size=%d", len(q.Set))
	} else {
		values := make([]string, 0, len(q.Set))
		for v := range q.Set {
			values = append(values, v)
		}
		sort.Strings(values)
		detail = strings.Join(values, " ")
	}
	return fmt.Sprintf("(filenameset %s)", detail)
}

func NewFileNameSet(fileNames ...string) *FileNameSet {
	s := &FileNameSet{Set: make(map[string]struct{})}
	for _, r := range fileNames {
		s.Set[r] = struct{}{}
	}
	return s
}

const (
	TypeFileMatch uint8 = iota
	TypeFileName
	TypeRepo
)

// Type changes the result type returned.
type Type struct {
	Child Q
	Type  uint8
}

func TypeFromProto(p *v1.Type) (*Type, error) {
	child, err := QFromProto(p.GetChild())
	if err != nil {
		return nil, err
	}

	return &Type{
		Child: child,
		// TODO: make proper enum types
		Type: uint8(p.GetType()),
	}, nil
}

func (q *Type) ToProto() *v1.Type {
	return &v1.Type{
		Child: QToProto(q.Child),
		Type:  uint32(q.Type),
	}
}

func (q *Type) String() string {
	switch q.Type {
	case TypeFileMatch:
		return fmt.Sprintf("(type:filematch %s)", q.Child)
	case TypeFileName:
		return fmt.Sprintf("(type:filename %s)", q.Child)
	case TypeRepo:
		return fmt.Sprintf("(type:repo %s)", q.Child)
	default:
		return fmt.Sprintf("(type:UNKNOWN %s)", q.Child)
	}
}

// Substring is the most basic query: a query for a substring.
type Substring struct {
	Pattern       string
	CaseSensitive bool

	// Match only filename
	FileName bool

	// Match only content
	Content bool
}

func SubstringFromProto(p *v1.Substring) *Substring {
	return &Substring{
		Pattern:       p.GetPattern(),
		CaseSensitive: p.GetCaseSensitive(),
		FileName:      p.GetFileName(),
		Content:       p.GetContent(),
	}
}

func (q *Substring) ToProto() *v1.Substring {
	return &v1.Substring{
		Pattern:       q.Pattern,
		CaseSensitive: q.CaseSensitive,
		FileName:      q.FileName,
		Content:       q.Content,
	}
}

func (q *Substring) String() string {
	s := ""

	t := ""
	if q.FileName {
		t = "file_"
	} else if q.Content {
		t = "content_"
	}

	s += fmt.Sprintf("%ssubstr:%q", t, q.Pattern)
	if q.CaseSensitive {
		s = "case_" + s
	}
	return s
}

type setCaser interface {
	setCase(string)
}

func (q *Substring) setCase(k string) {
	switch k {
	case "yes":
		q.CaseSensitive = true
	case "no":
		q.CaseSensitive = false
	case "auto":
		// TODO - unicode
		q.CaseSensitive = (q.Pattern != string(toLower([]byte(q.Pattern))))
	}
}

func (q *Symbol) setCase(k string) {
	if sc, ok := q.Expr.(setCaser); ok {
		sc.setCase(k)
	}
}

func (q *Regexp) setCase(k string) {
	switch k {
	case "yes":
		q.CaseSensitive = true
	case "no":
		q.CaseSensitive = false
	case "auto":
		q.CaseSensitive = (q.Regexp.String() != LowerRegexp(q.Regexp).String())
	}
}

// GobCache exists so we only pay the cost of marshalling a query once when we
// aggregate it out over all the replicas.
//
// Our query and eval layer do not support GobCache. Instead, at the gob
// boundaries (RPC and Streaming) we check if the Q is a GobCache and unwrap
// it.
//
// "I wish we could get rid of this code soon enough" - tomas
type GobCache struct {
	Q

	once sync.Once
	data []byte
	err  error
}

// GobEncode implements gob.Encoder.
func (q *GobCache) GobEncode() ([]byte, error) {
	q.once.Do(func() {
		var buf bytes.Buffer
		enc := gob.NewEncoder(&buf)
		q.err = enc.Encode(&gobWrapper{
			WrappedQ: q.Q,
		})
		q.data = buf.Bytes()
	})
	return q.data, q.err
}

// GobDecode implements gob.Decoder.
func (q *GobCache) GobDecode(data []byte) error {
	dec := gob.NewDecoder(bytes.NewBuffer(data))
	var w gobWrapper
	err := dec.Decode(&w)
	if err != nil {
		return err
	}
	q.Q = w.WrappedQ
	return nil
}

// gobWrapper is needed so the gob decoder works.
type gobWrapper struct {
	WrappedQ Q
}

func (q *GobCache) String() string {
	return fmt.Sprintf("GobCache(%s)", q.Q)
}

// Or is matched when any of its children is matched.
type Or struct {
	Children []Q
}

func OrFromProto(p *v1.Or) (*Or, error) {
	children := make([]Q, len(p.GetChildren()))
	for i, child := range p.GetChildren() {
		c, err := QFromProto(child)
		if err != nil {
			return nil, err
		}
		children[i] = c
	}
	return &Or{
		Children: children,
	}, nil
}

func (q *Or) ToProto() *v1.Or {
	children := make([]*v1.Q, len(q.Children))
	for i, child := range q.Children {
		children[i] = QToProto(child)
	}
	return &v1.Or{
		Children: children,
	}
}

func (q *Or) String() string {
	var sub []string
	for _, ch := range q.Children {
		sub = append(sub, ch.String())
	}
	return fmt.Sprintf("(or %s)", strings.Join(sub, " "))
}

// Not inverts the meaning of its child.
type Not struct {
	Child Q
}

func NotFromProto(p *v1.Not) (*Not, error) {
	child, err := QFromProto(p.GetChild())
	if err != nil {
		return nil, err
	}
	return &Not{
		Child: child,
	}, nil
}

func (q *Not) ToProto() *v1.Not {
	return &v1.Not{
		Child: QToProto(q.Child),
	}
}

func (q *Not) String() string {
	return fmt.Sprintf("(not %s)", q.Child)
}

// And is matched when all its children are.
type And struct {
	Children []Q
}

func AndFromProto(p *v1.And) (*And, error) {
	children := make([]Q, len(p.GetChildren()))
	for i, child := range p.GetChildren() {
		c, err := QFromProto(child)
		if err != nil {
			return nil, err
		}
		children[i] = c
	}
	return &And{
		Children: children,
	}, nil
}

func (q *And) ToProto() *v1.And {
	children := make([]*v1.Q, len(q.Children))
	for i, child := range q.Children {
		children[i] = QToProto(child)
	}
	return &v1.And{
		Children: children,
	}
}

func (q *And) String() string {
	var sub []string
	for _, ch := range q.Children {
		sub = append(sub, ch.String())
	}
	return fmt.Sprintf("(and %s)", strings.Join(sub, " "))
}

// NewAnd is syntactic sugar for constructing And queries.
func NewAnd(qs ...Q) Q {
	return &And{Children: qs}
}

// NewOr is syntactic sugar for constructing Or queries.
func NewOr(qs ...Q) Q {
	return &Or{Children: qs}
}

// Branch limits search to a specific branch.
type Branch struct {
	Pattern string

	// exact is true if we want to Pattern to equal branch.
	Exact bool
}

func BranchFromProto(p *v1.Branch) *Branch {
	return &Branch{
		Pattern: p.GetPattern(),
		Exact:   p.GetExact(),
	}
}

func (q *Branch) ToProto() *v1.Branch {
	return &v1.Branch{
		Pattern: q.Pattern,
		Exact:   q.Exact,
	}
}

func (q *Branch) String() string {
	if q.Exact {
		return fmt.Sprintf("branch=%q", q.Pattern)
	}
	return fmt.Sprintf("branch:%q", q.Pattern)
}

func queryChildren(q Q) []Q {
	switch s := q.(type) {
	case *And:
		return s.Children
	case *Or:
		return s.Children
	}
	return nil
}

func flattenAndOr(children []Q, typ Q) ([]Q, bool) {
	var flat []Q
	changed := false
	for _, ch := range children {
		ch, subChanged := flatten(ch)
		changed = changed || subChanged
		if reflect.TypeOf(ch) == reflect.TypeOf(typ) {
			changed = true
			subChildren := queryChildren(ch)
			if subChildren != nil {
				flat = append(flat, subChildren...)
			}
		} else {
			flat = append(flat, ch)
		}
	}

	return flat, changed
}

// (and (and x y) z) => (and x y z) , the same for "or"
func flatten(q Q) (Q, bool) {
	switch s := q.(type) {
	case *And:
		if len(s.Children) == 1 {
			return s.Children[0], true
		}
		flatChildren, changed := flattenAndOr(s.Children, s)
		return &And{flatChildren}, changed
	case *Or:
		if len(s.Children) == 1 {
			return s.Children[0], true
		}
		flatChildren, changed := flattenAndOr(s.Children, s)
		return &Or{flatChildren}, changed
	case *Not:
		child, changed := flatten(s.Child)
		return &Not{child}, changed
	case *Type:
		child, changed := flatten(s.Child)
		return &Type{Child: child, Type: s.Type}, changed
	default:
		return q, false
	}
}

func mapQueryList(qs []Q, f func(Q) Q) []Q {
	neg := make([]Q, len(qs))
	for i, sub := range qs {
		neg[i] = Map(sub, f)
	}
	return neg
}

func invertConst(q Q) Q {
	c, ok := q.(*Const)
	if ok {
		return &Const{!c.Value}
	}
	return q
}

func evalAndOrConstants(q Q, children []Q) Q {
	_, isAnd := q.(*And)

	children = mapQueryList(children, evalConstants)

	newCH := children[:0]
	for _, ch := range children {
		c, ok := ch.(*Const)
		if ok {
			if c.Value == isAnd {
				continue
			} else {
				return ch
			}
		}
		newCH = append(newCH, ch)
	}
	if len(newCH) == 0 {
		return &Const{isAnd}
	}
	if isAnd {
		return &And{newCH}
	}
	return &Or{newCH}
}

func evalConstants(q Q) Q {
	switch s := q.(type) {
	case *And:
		return evalAndOrConstants(q, s.Children)
	case *Or:
		return evalAndOrConstants(q, s.Children)
	case *Not:
		ch := evalConstants(s.Child)
		if _, ok := ch.(*Const); ok {
			return invertConst(ch)
		}
		return &Not{ch}
	case *Type:
		ch := evalConstants(s.Child)
		if _, ok := ch.(*Const); ok {
			// If q is the root query, then evaluating this to a const changes
			// the type of result we will return. However, the only case this
			// makes sense is `type:repo TRUE` to return all repos or
			// `type:file TRUE` to return all filenames. For other cases we
			// want to do this constant folding though, so we allow the
			// unexpected behaviour mentioned previously.
			return ch
		}
		return &Type{Child: ch, Type: s.Type}
	case *Substring:
		if len(s.Pattern) == 0 {
			return &Const{true}
		}
	case *Regexp:
		if s.Regexp.Op == syntax.OpEmptyMatch {
			return &Const{true}
		}
	case *Branch:
		if s.Pattern == "" {
			return &Const{true}
		}
	case *RepoSet:
		if len(s.Set) == 0 {
			return &Const{false}
		}
	case *FileNameSet:
		if len(s.Set) == 0 {
			return &Const{false}
		}
	}
	return q
}

func Simplify(q Q) Q {
	q = evalConstants(q)
	for {
		var changed bool
		q, changed = flatten(q)
		if !changed {
			break
		}
	}

	return q
}

// Map runs f over the q.
func Map(q Q, f func(q Q) Q) Q {
	switch s := q.(type) {
	case *And:
		q = &And{Children: mapQueryList(s.Children, f)}
	case *Or:
		q = &Or{Children: mapQueryList(s.Children, f)}
	case *Not:
		q = &Not{Child: Map(s.Child, f)}
	case *Type:
		q = &Type{Type: s.Type, Child: Map(s.Child, f)}
	}
	return f(q)
}

// Expand expands Substr queries into (OR file_substr content_substr)
// queries, and the same for Regexp queries..
func ExpandFileContent(q Q) Q {
	switch s := q.(type) {
	case *Substring:
		if s.FileName == s.Content {
			f := *s
			f.FileName = true
			f.Content = false
			c := *s
			c.FileName = false
			c.Content = true
			return NewOr(&f, &c)
		}
	case *Regexp:
		if s.FileName == s.Content {
			f := *s
			f.FileName = true
			f.Content = false
			c := *s
			c.FileName = false
			c.Content = true
			return NewOr(&f, &c)
		}
	}
	return q
}

// VisitAtoms runs `v` on all atom queries within `q`.
func VisitAtoms(q Q, v func(q Q)) {
	Map(q, func(iQ Q) Q {
		switch iQ.(type) {
		case *And:
		case *Or:
		case *Not:
		case *Type:
		default:
			v(iQ)
		}
		return iQ
	})
}

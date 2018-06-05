// Copyright 2015 go-swagger maintainers
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

package generator

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-openapi/analysis"
	"github.com/go-openapi/loads"
	"github.com/go-openapi/runtime"
	"github.com/go-openapi/spec"
	"github.com/go-openapi/swag"
)

type respSort struct {
	Code     int
	Response spec.Response
}

type responses []respSort

func (s responses) Len() int           { return len(s) }
func (s responses) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }
func (s responses) Less(i, j int) bool { return s[i].Code < s[j].Code }

func sortedResponses(input map[int]spec.Response) responses {
	var res responses
	for k, v := range input {
		if k > 0 {
			res = append(res, respSort{k, v})
		}
	}
	sort.Sort(res)
	return res
}

// GenerateServerOperation generates a parameter model, parameter validator, http handler implementations for a given operation
// It also generates an operation handler interface that uses the parameter model for handling a valid request.
// Allows for specifying a list of tags to include only certain tags for the generation
func GenerateServerOperation(operationNames []string, opts *GenOpts) error {
	if opts == nil {
		return errors.New("gen opts are required")
	}
	templates.LoadDefaults()
	if opts.TemplateDir != "" {
		if err := templates.LoadDir(opts.TemplateDir); err != nil {
			return err
		}
	}

	// Load the spec
	_, specDoc, err := loadSpec(opts.Spec)
	if err != nil {
		return err
	}

	// Validate and Expand. specDoc is in/out param.
	specDoc, err = validateAndFlattenSpec(opts, specDoc)
	if err != nil {
		return err
	}

	analyzed := analysis.New(specDoc.Spec())

	ops := gatherOperations(analyzed, operationNames)
	if len(ops) == 0 {
		return errors.New("no operations were selected")
	}

	for operationName, opRef := range ops {
		method, path, operation := opRef.Method, opRef.Path, opRef.Op
		defaultScheme := opts.DefaultScheme
		if defaultScheme == "" {
			defaultScheme = sHTTP
		}
		defaultProduces := opts.DefaultProduces
		if defaultProduces == "" {
			defaultProduces = runtime.JSONMime
		}
		defaultConsumes := opts.DefaultConsumes
		if defaultConsumes == "" {
			defaultConsumes = runtime.JSONMime
		}

		apiPackage := opts.LanguageOpts.MangleName(swag.ToFileName(opts.APIPackage), "api")
		serverPackage := opts.LanguageOpts.MangleName(swag.ToFileName(opts.ServerPackage), "server")
		generator := operationGenerator{
			Name:                 operationName,
			Method:               method,
			Path:                 path,
			BasePath:             specDoc.BasePath(),
			APIPackage:           apiPackage,
			ModelsPackage:        opts.LanguageOpts.MangleName(swag.ToFileName(opts.ModelPackage), "definitions"),
			ClientPackage:        opts.LanguageOpts.MangleName(swag.ToFileName(opts.ClientPackage), "client"),
			ServerPackage:        serverPackage,
			Operation:            *operation,
			SecurityRequirements: analyzed.SecurityRequirementsFor(operation),
			SecurityDefinitions:  analyzed.SecurityDefinitionsFor(operation),
			Principal:            opts.Principal,
			Target:               filepath.Join(opts.Target, serverPackage),
			Base:                 opts.Target,
			Tags:                 opts.Tags,
			IncludeHandler:       opts.IncludeHandler,
			IncludeParameters:    opts.IncludeParameters,
			IncludeResponses:     opts.IncludeResponses,
			IncludeValidator:     true, // we no more support the CLI option to disable validation
			DumpData:             opts.DumpData,
			DefaultScheme:        defaultScheme,
			DefaultProduces:      defaultProduces,
			DefaultConsumes:      defaultConsumes,
			Doc:                  specDoc,
			Analyzed:             analyzed,
			GenOpts:              opts,
		}
		if err := generator.Generate(); err != nil {
			return err
		}
	}
	return nil
}

type operationGenerator struct {
	Authorized        bool
	IncludeHandler    bool
	IncludeParameters bool
	IncludeResponses  bool
	IncludeValidator  bool
	DumpData          bool
	WithContext       bool

	Principal            string
	Target               string
	Base                 string
	Name                 string
	Method               string
	Path                 string
	BasePath             string
	APIPackage           string
	ModelsPackage        string
	ServerPackage        string
	ClientPackage        string
	Operation            spec.Operation
	SecurityRequirements [][]analysis.SecurityRequirement
	SecurityDefinitions  map[string]spec.SecurityScheme
	Tags                 []string
	DefaultScheme        string
	DefaultProduces      string
	DefaultConsumes      string
	Doc                  *loads.Document
	Analyzed             *analysis.Spec
	GenOpts              *GenOpts
}

func intersectTags(left, right []string) (filtered []string) {
	if len(right) == 0 {
		filtered = left[:]
		return
	}
	for _, l := range left {
		if containsString(right, l) {
			filtered = append(filtered, l)
		}
	}
	return
}

func (o *operationGenerator) Generate() error {
	// Build a list of codegen operations based on the tags,
	// the tag decides the actual package for an operation
	// the user specified package serves as root for generating the directory structure
	var operations GenOperations
	authed := len(o.SecurityRequirements) > 0

	var bldr codeGenOpBuilder
	bldr.Name = o.Name
	bldr.Method = o.Method
	bldr.Path = o.Path
	bldr.BasePath = o.BasePath
	bldr.ModelsPackage = o.ModelsPackage
	bldr.Principal = o.Principal
	bldr.Target = o.Target
	bldr.Operation = o.Operation
	bldr.Authed = authed
	bldr.Security = o.SecurityRequirements
	bldr.SecurityDefinitions = o.SecurityDefinitions
	bldr.Doc = o.Doc
	bldr.Analyzed = o.Analyzed
	bldr.DefaultScheme = o.DefaultScheme
	bldr.DefaultProduces = o.DefaultProduces
	bldr.RootAPIPackage = o.APIPackage
	bldr.WithContext = o.WithContext
	bldr.GenOpts = o.GenOpts
	bldr.DefaultConsumes = o.DefaultConsumes
	bldr.IncludeValidator = o.IncludeValidator

	bldr.DefaultImports = []string{o.GenOpts.ExistingModels}
	if o.GenOpts.ExistingModels == "" {
		bldr.DefaultImports = []string{filepath.ToSlash(filepath.Join(o.GenOpts.LanguageOpts.baseImport(o.Base), o.ModelsPackage))}
	}

	bldr.APIPackage = bldr.RootAPIPackage
	st := o.Tags
	if o.GenOpts != nil {
		st = o.GenOpts.Tags
	}
	intersected := intersectTags(o.Operation.Tags, st)
	if len(intersected) == 1 {
		tag := intersected[0]
		bldr.APIPackage = o.GenOpts.LanguageOpts.MangleName(swag.ToFileName(tag), o.APIPackage)
	}
	op, err := bldr.MakeOperation()
	if err != nil {
		return err
	}
	op.Tags = intersected
	operations = append(operations, op)
	sort.Sort(operations)

	for _, op := range operations {
		if o.GenOpts.DumpData {
			bb, _ := json.MarshalIndent(swag.ToDynamicJSON(op), "", " ")
			fmt.Fprintln(os.Stdout, string(bb))
			continue
		}
		if err := o.GenOpts.renderOperation(&op); err != nil {
			return err
		}
	}

	return nil
}

type codeGenOpBuilder struct {
	WithContext      bool
	Authed           bool
	IncludeValidator bool

	Name                string
	Method              string
	Path                string
	BasePath            string
	APIPackage          string
	RootAPIPackage      string
	ModelsPackage       string
	Principal           string
	Target              string
	Operation           spec.Operation
	Doc                 *loads.Document
	Analyzed            *analysis.Spec
	DefaultImports      []string
	Imports             map[string]string
	DefaultScheme       string
	DefaultProduces     string
	DefaultConsumes     string
	Security            [][]analysis.SecurityRequirement
	SecurityDefinitions map[string]spec.SecurityScheme
	ExtraSchemas        map[string]GenSchema
	GenOpts             *GenOpts
}

func renameTimeout(seenIds map[string][]string, current string) string {
	var next string
	switch strings.ToLower(current) {
	case "timeout":
		next = "requestTimeout"
	case "requesttimeout":
		next = "httpRequestTimeout"
	case "httptrequesttimeout":
		next = "swaggerTimeout"
	case "swaggertimeout":
		next = "operationTimeout"
	case "operationtimeout":
		next = "opTimeout"
	case "optimeout":
		next = "operTimeout"
	}
	if _, ok := seenIds[next]; ok {
		return renameTimeout(seenIds, next)
	}
	return next
}

func (b *codeGenOpBuilder) MakeOperation() (GenOperation, error) {
	if Debug {
		log.Printf("[%s %s] parsing operation (id: %q)", b.Method, b.Path, b.Operation.ID)
	}
	// NOTE: we assume flatten is enabled by default (i.e. complex constructs are resolved from the models package),
	// but do not assume the spec is necessarily fully flattened (i.e. all schemas moved to definitions).
	// Flattened means that all complex constructs are present as
	// definitions and models produced accordingly in ModelsPackage.
	// When this is not the case, extra schemas are produced in the operations package.
	//
	// In all cases, resetting definitions to the _original_ (untransformed) spec is not an option:
	// we take it from here the spec possibly already transformed by the GenDefinitions stage.
	resolver := newTypeResolver(b.ModelsPackage, b.Doc)
	receiver := "o"

	operation := b.Operation
	var params, qp, pp, hp, fp GenParameters
	var hasQueryParams, hasPathParams, hasHeaderParams, hasFormParams, hasFileParams, hasFormValueParams, hasBodyParams bool
	paramsForOperation := b.Analyzed.ParamsFor(b.Method, b.Path)
	timeoutName := "timeout"

	idMapping := map[string]map[string]string{
		"query":    make(map[string]string, len(paramsForOperation)),
		"path":     make(map[string]string, len(paramsForOperation)),
		"formData": make(map[string]string, len(paramsForOperation)),
		"header":   make(map[string]string, len(paramsForOperation)),
		"body":     make(map[string]string, len(paramsForOperation)),
	}

	seenIds := make(map[string][]string, len(paramsForOperation))
	for id, p := range paramsForOperation {
		if _, ok := seenIds[p.Name]; ok {
			idMapping[p.In][p.Name] = swag.ToGoName(id)
		} else {
			idMapping[p.In][p.Name] = swag.ToGoName(p.Name)
		}
		seenIds[p.Name] = append(seenIds[p.Name], p.In)
		if strings.ToLower(p.Name) == strings.ToLower(timeoutName) {
			timeoutName = renameTimeout(seenIds, timeoutName)
		}
	}

	for _, p := range paramsForOperation {
		cp, err := b.MakeParameter(receiver, resolver, p, idMapping)

		if err != nil {
			return GenOperation{}, err
		}
		if cp.IsQueryParam() {
			hasQueryParams = true
			qp = append(qp, cp)
		}
		if cp.IsFormParam() {
			if p.Type == file {
				hasFileParams = true
			}
			if p.Type != file {
				hasFormValueParams = true
			}
			hasFormParams = true
			fp = append(fp, cp)
		}
		if cp.IsPathParam() {
			hasPathParams = true
			pp = append(pp, cp)
		}
		if cp.IsHeaderParam() {
			hasHeaderParams = true
			hp = append(hp, cp)
		}
		if cp.IsBodyParam() {
			hasBodyParams = true
		}
		params = append(params, cp)
	}
	sort.Sort(params)
	sort.Sort(qp)
	sort.Sort(pp)
	sort.Sort(hp)
	sort.Sort(fp)

	var srs responses
	if operation.Responses != nil {
		srs = sortedResponses(operation.Responses.StatusCodeResponses)
	}
	responses := make([]GenResponse, 0, len(srs))
	var defaultResponse *GenResponse
	var successResponses []GenResponse
	if operation.Responses != nil {
		for _, v := range srs {
			name, ok := v.Response.Extensions.GetString(xGoName)
			if !ok {
				name = runtime.Statuses[v.Code]
			}
			name = swag.ToJSONName(b.Name + " " + name)
			isSuccess := v.Code/100 == 2
			gr, err := b.MakeResponse(receiver, name, isSuccess, resolver, v.Code, v.Response)
			if err != nil {
				return GenOperation{}, err
			}
			if isSuccess {
				successResponses = append(successResponses, gr)
			}
			responses = append(responses, gr)
		}

		if operation.Responses.Default != nil {
			gr, err := b.MakeResponse(receiver, b.Name+" default", false, resolver, -1, *operation.Responses.Default)
			if err != nil {
				return GenOperation{}, err
			}
			defaultResponse = &gr
		}
	}
	// Always render a default response, even when no responses were defined
	if operation.Responses == nil || (operation.Responses.Default == nil && len(srs) == 0) {
		gr, err := b.MakeResponse(receiver, b.Name+" default", false, resolver, -1, spec.Response{})
		if err != nil {
			return GenOperation{}, err
		}
		defaultResponse = &gr
	}

	if b.Principal == "" {
		b.Principal = iface
	}

	swsp := resolver.Doc.Spec()
	var extraSchemes []string
	if ess, ok := operation.Extensions.GetStringSlice(xSchemes); ok {
		extraSchemes = append(extraSchemes, ess...)
	}

	if ess1, ok := swsp.Extensions.GetStringSlice(xSchemes); ok {
		extraSchemes = concatUnique(ess1, extraSchemes)
	}
	sort.Strings(extraSchemes)
	schemes := concatUnique(swsp.Schemes, operation.Schemes)
	sort.Strings(schemes)
	produces := producesOrDefault(operation.Produces, swsp.Produces, b.DefaultProduces)
	sort.Strings(produces)
	consumes := producesOrDefault(operation.Consumes, swsp.Consumes, b.DefaultConsumes)
	sort.Strings(consumes)

	var hasStreamingResponse bool
	if defaultResponse != nil && defaultResponse.Schema != nil && defaultResponse.Schema.IsStream {
		hasStreamingResponse = true
	}
	var successResponse *GenResponse
	for _, sr := range successResponses {
		if sr.IsSuccess {
			successResponse = &sr
			break
		}
	}
	for _, sr := range successResponses {
		if !hasStreamingResponse && sr.Schema != nil && sr.Schema.IsStream {
			hasStreamingResponse = true
			break
		}
	}
	if !hasStreamingResponse {
		for _, r := range responses {
			if r.Schema != nil && r.Schema.IsStream {
				hasStreamingResponse = true
				break
			}
		}
	}

	return GenOperation{
		GenCommon: GenCommon{
			Copyright:        b.GenOpts.Copyright,
			TargetImportPath: filepath.ToSlash(b.GenOpts.LanguageOpts.baseImport(b.GenOpts.Target)),
		},
		Package:              b.APIPackage,
		RootPackage:          b.RootAPIPackage,
		Name:                 b.Name,
		Method:               b.Method,
		Path:                 b.Path,
		BasePath:             b.BasePath,
		Tags:                 operation.Tags[:],
		Description:          trimBOM(operation.Description),
		ReceiverName:         receiver,
		DefaultImports:       b.DefaultImports,
		Imports:              b.Imports,
		Params:               params,
		Summary:              trimBOM(operation.Summary),
		QueryParams:          qp,
		PathParams:           pp,
		HeaderParams:         hp,
		FormParams:           fp,
		HasQueryParams:       hasQueryParams,
		HasPathParams:        hasPathParams,
		HasHeaderParams:      hasHeaderParams,
		HasFormParams:        hasFormParams,
		HasFormValueParams:   hasFormValueParams,
		HasFileParams:        hasFileParams,
		HasBodyParams:        hasBodyParams,
		HasStreamingResponse: hasStreamingResponse,
		Authorized:           b.Authed,
		Security:             b.makeSecurityRequirements(receiver),
		SecurityDefinitions:  b.makeSecuritySchemes(receiver),
		Principal:            b.Principal,
		Responses:            responses,
		DefaultResponse:      defaultResponse,
		SuccessResponse:      successResponse,
		SuccessResponses:     successResponses,
		ExtraSchemas:         gatherExtraSchemas(b.ExtraSchemas),
		Schemes:              schemeOrDefault(schemes, b.DefaultScheme),
		ProducesMediaTypes:   produces,
		ConsumesMediaTypes:   consumes,
		ExtraSchemes:         extraSchemes,
		WithContext:          b.WithContext,
		TimeoutName:          timeoutName,
		Extensions:           operation.Extensions,
	}, nil
}

func producesOrDefault(produces []string, fallback []string, defaultProduces string) []string {
	if len(produces) > 0 {
		return produces
	}
	if len(fallback) > 0 {
		return fallback
	}
	return []string{defaultProduces}
}

func schemeOrDefault(schemes []string, defaultScheme string) []string {
	if len(schemes) == 0 {
		return []string{defaultScheme}
	}
	return schemes
}

func concatUnique(collections ...[]string) []string {
	resultSet := make(map[string]struct{})
	for _, c := range collections {
		for _, i := range c {
			if _, ok := resultSet[i]; !ok {
				resultSet[i] = struct{}{}
			}
		}
	}
	var result []string
	for k := range resultSet {
		result = append(result, k)
	}
	return result
}

func (b *codeGenOpBuilder) MakeResponse(receiver, name string, isSuccess bool, resolver *typeResolver, code int, resp spec.Response) (GenResponse, error) {
	if Debug {
		log.Printf("[%s %s] making id %q", b.Method, b.Path, b.Operation.ID)
	}

	if resp.Ref.String() != "" {
		resp2, err := spec.ResolveResponse(b.Doc.Spec(), resp.Ref)
		if err != nil {
			return GenResponse{}, err
		}
		if resp2 == nil {
			return GenResponse{}, fmt.Errorf("could not resolve response ref: %s", resp.Ref.String())
		}
		resp = *resp2
	}

	res := GenResponse{
		Package:        b.APIPackage,
		ModelsPackage:  b.ModelsPackage,
		ReceiverName:   receiver,
		Name:           name,
		Description:    trimBOM(resp.Description),
		DefaultImports: b.DefaultImports,
		Imports:        b.Imports,
		IsSuccess:      isSuccess,
		Code:           code,
		Method:         b.Method,
		Path:           b.Path,
		Extensions:     resp.Extensions,
	}

	for hName, header := range resp.Headers {
		hdr, err := b.MakeHeader(receiver, hName, header)
		if err != nil {
			return GenResponse{}, err
		}
		res.Headers = append(res.Headers, hdr)
	}
	sort.Sort(res.Headers)

	if resp.Schema != nil {
		var schema GenSchema
		var named bool
		rslv := resolver
		sch := resp.Schema
		if resp.Schema.Ref.String() != "" && !resp.Schema.Ref.HasFragmentOnly {
			// This section attempts to resolve external $ref (e.g. which are not only a fragment)
			// TODO: this is currently unused as always resolved beforehand by flattening/expanding the spec.
			// We should guard the generator against complex $ref, such as external, or pointing to anonymous places.
			ss, err := spec.ResolveRefWithBase(b.Doc.Spec(), &resp.Schema.Ref, nil)
			if err != nil {
				return GenResponse{}, err
			}
			sch = ss
			named = true
			rslv = resolver.NewWithModelName(name + "Body")
		}

		sc := schemaGenContext{
			Path:             fmt.Sprintf("%q", name),
			Name:             name + "Body",
			Receiver:         receiver,
			ValueExpr:        receiver,
			IndexVar:         "i",
			Schema:           *sch,
			Required:         !named,
			TypeResolver:     rslv,
			Named:            named,
			ExtraSchemas:     make(map[string]GenSchema),
			IncludeModel:     true,
			IncludeValidator: true,
		}
		br, bs := b.saveResolveContext(rslv, sch)
		if err := sc.makeGenSchema(); err != nil {
			return GenResponse{}, err
		}

		if !strings.HasPrefix(sch.Ref.String(), "#/definitions") && len(sc.ExtraSchemas) > 0 {
			// when some ExtraSchemas are produced from something else than a definition,
			// this indicates we are not running in fully flatten mode and we need to render
			// ExtraSchemas in the operation's package.
			// We need to rebuild the schema with a new type resolver to reflect this change in the
			// models package.
			//
			// In the following, we want to produce extra schemas in the current package (operations),
			// while being able to reuse already resolved definitions in the models package.

			// restore resolving state before previous call to makeGenSchema()
			rslv = br
			sc.Schema = *bs

			pg := sc.shallowClone()
			pg.TypeResolver = newTypeResolver("", rslv.Doc).withKeepDefinitionsPackage(resolver.ModelsPackage)
			pg.ExtraSchemas = make(map[string]GenSchema, len(sc.ExtraSchemas))
			rslv = pg.TypeResolver

			if err := pg.makeGenSchema(); err != nil {
				return GenResponse{}, err
			}
			// lift nested extra schemas (inlined types)
			if b.ExtraSchemas == nil {
				b.ExtraSchemas = make(map[string]GenSchema, len(pg.ExtraSchemas))
			}
			for _, v := range pg.ExtraSchemas {
				vv := v
				vv.GoType = vv.Name
				vv.IsAnonymous = false
				if !v.IsStream {
					b.ExtraSchemas[vv.Name] = vv
				}
			}
			schema = pg.GenSchema
		} else {
			schema = sc.GenSchema
		}

		if named {
			// TODO: this is currently unused as always resolved beforehand by flattening/expanding the spec.
			if b.ExtraSchemas == nil {
				b.ExtraSchemas = make(map[string]GenSchema)
			}
			if !schema.IsStream {
				b.ExtraSchemas[schema.Name] = schema
			}
		}
		if schema.IsAnonymous {
			schema.Name = swag.ToGoName(sc.Name)
			nm := schema.Name
			if b.ExtraSchemas == nil {
				b.ExtraSchemas = make(map[string]GenSchema)
			}
			if !schema.IsStream {
				b.ExtraSchemas[schema.Name] = schema
			}
			schema = GenSchema{}
			schema.IsAnonymous = false
			schema.GoType = rslv.goTypeName(nm)
			schema.SwaggerType = nm
		}

		res.Schema = &schema
	}
	return res, nil
}

func (b *codeGenOpBuilder) MakeHeader(receiver, name string, hdr spec.Header) (GenHeader, error) {
	tpe := typeForHeader(hdr) //simpleResolvedType(hdr.Type, hdr.Format, hdr.Items)

	id := swag.ToGoName(name)
	res := GenHeader{
		sharedValidations: sharedValidationsFromSimple(hdr.CommonValidations, true), // NOTE: Required is not defined by the Swagger schema for header. Set arbitrarily to true for convenience in templates.
		resolvedType:      tpe,
		Package:           b.APIPackage,
		ReceiverName:      receiver,
		ID:                id,
		Name:              name,
		Path:              fmt.Sprintf("%q", name),
		ValueExpression:   fmt.Sprintf("%s.%s", receiver, id),
		Description:       trimBOM(hdr.Description),
		Default:           hdr.Default,
		HasDefault:        hdr.Default != nil,
		Converter:         stringConverters[tpe.GoType],
		Formatter:         stringFormatters[tpe.GoType],
		ZeroValue:         tpe.Zero(),
		CollectionFormat:  hdr.CollectionFormat,
		IndexVar:          "i",
	}
	res.HasValidations, res.HasSliceValidations = b.HasValidations(hdr.CommonValidations, res.resolvedType)

	hasChildValidations := false
	if hdr.Items != nil {
		pi, err := b.MakeHeaderItem(receiver, name+" "+res.IndexVar, res.IndexVar+"i", "fmt.Sprintf(\"%s.%v\", \"header\", "+res.IndexVar+")", res.Name+"I", hdr.Items, nil)
		if err != nil {
			return GenHeader{}, err
		}
		res.Child = &pi
		hasChildValidations = pi.HasValidations
	}
	// we feed the GenHeader structure the same way as we do for
	// GenParameter, even though there is currently no actual validation
	// for response headers.
	res.HasValidations = res.HasValidations || hasChildValidations

	return res, nil
}

func (b *codeGenOpBuilder) MakeHeaderItem(receiver, paramName, indexVar, path, valueExpression string, items, parent *spec.Items) (GenItems, error) {
	var res GenItems
	res.resolvedType = simpleResolvedType(items.Type, items.Format, items.Items)
	res.sharedValidations = sharedValidationsFromSimple(items.CommonValidations, false)
	res.Name = paramName
	res.Path = path
	res.Location = "header"
	res.ValueExpression = swag.ToVarName(valueExpression)
	res.CollectionFormat = items.CollectionFormat
	res.Converter = stringConverters[res.GoType]
	res.Formatter = stringFormatters[res.GoType]
	res.IndexVar = indexVar
	res.HasValidations, res.HasSliceValidations = b.HasValidations(items.CommonValidations, res.resolvedType)

	if items.Items != nil {
		// Recursively follows nested arrays
		// IMPORTANT! transmitting a ValueExpression consistent with the parent's one
		hi, err := b.MakeHeaderItem(receiver, paramName+" "+indexVar, indexVar+"i", "fmt.Sprintf(\"%s.%v\", \"header\", "+indexVar+")", res.ValueExpression+"I", items.Items, items)
		if err != nil {
			return GenItems{}, err
		}
		res.Child = &hi
		hi.Parent = &res
		// Propagates HasValidations flag to outer Items definition (currently not in use: done to remain consistent with parameters)
		res.HasValidations = res.HasValidations || hi.HasValidations
	}

	return res, nil
}

// HasValidations resolves the validation status for simple schema objects
func (b *codeGenOpBuilder) HasValidations(sh spec.CommonValidations, rt resolvedType) (hasValidations bool, hasSliceValidations bool) {
	// TODO: exclude format byte (istrfmt.Base64) from validation (issue#1548)
	hasNumberValidation := sh.Maximum != nil || sh.Minimum != nil || sh.MultipleOf != nil
	hasStringValidation := sh.MaxLength != nil || sh.MinLength != nil || sh.Pattern != ""
	hasSliceValidations = sh.MaxItems != nil || sh.MinItems != nil || sh.UniqueItems || len(sh.Enum) > 0
	hasValidations = (hasNumberValidation || hasStringValidation || hasSliceValidations || rt.IsCustomFormatter) && !rt.IsStream && !rt.IsInterface
	return
}

func (b *codeGenOpBuilder) MakeParameterItem(receiver, paramName, indexVar, path, valueExpression, location string, resolver *typeResolver, items, parent *spec.Items) (GenItems, error) {
	debugLog("making parameter item recv=%s param=%s index=%s valueExpr=%s path=%s location=%s", receiver, paramName, indexVar, valueExpression, path, location)
	var res GenItems
	res.resolvedType = simpleResolvedType(items.Type, items.Format, items.Items)
	res.sharedValidations = sharedValidationsFromSimple(items.CommonValidations, false)
	res.Name = paramName
	res.Path = path
	res.Location = location
	res.ValueExpression = swag.ToVarName(valueExpression)
	res.CollectionFormat = items.CollectionFormat
	res.Converter = stringConverters[res.GoType]
	res.Formatter = stringFormatters[res.GoType]
	res.IndexVar = indexVar

	res.HasValidations, res.HasSliceValidations = b.HasValidations(items.CommonValidations, res.resolvedType)

	if items.Items != nil {
		// Recursively follows nested arrays
		// IMPORTANT! transmitting a ValueExpression consistent with the parent's one
		pi, err := b.MakeParameterItem(receiver, paramName+" "+indexVar, indexVar+"i", "fmt.Sprintf(\"%s.%v\", "+path+", "+indexVar+")", res.ValueExpression+"I", location, resolver, items.Items, items)
		if err != nil {
			return GenItems{}, err
		}
		res.Child = &pi
		pi.Parent = &res
		// Propagates HasValidations flag to outer Items definition
		res.HasValidations = res.HasValidations || pi.HasValidations
	}

	return res, nil
}

func (b *codeGenOpBuilder) MakeParameter(receiver string, resolver *typeResolver, param spec.Parameter, idMapping map[string]map[string]string) (GenParameter, error) {
	if Debug {
		log.Printf("[%s %s] making parameter %q", b.Method, b.Path, param.Name)
	}

	if param.Ref.String() != "" {
		// Resolve $ref before all
		param2, err := spec.ResolveParameter(b.Doc.Spec(), param.Ref)
		if err != nil {
			return GenParameter{}, err
		}
		if param2 == nil {
			return GenParameter{}, fmt.Errorf("could not resolve parameter ref: %s", param.Ref.String())
		}
		param = *param2
	}

	var child *GenItems
	id := swag.ToGoName(param.Name)
	if len(idMapping) > 0 {
		id = idMapping[param.In][param.Name]
	}

	res := GenParameter{
		ID:               id,
		Name:             param.Name,
		ModelsPackage:    b.ModelsPackage,
		Path:             fmt.Sprintf("%q", param.Name),
		ValueExpression:  fmt.Sprintf("%s.%s", receiver, id),
		IndexVar:         "i",
		BodyParam:        nil,
		Default:          param.Default,
		HasDefault:       param.Default != nil,
		Description:      trimBOM(param.Description),
		ReceiverName:     receiver,
		CollectionFormat: param.CollectionFormat,
		Child:            child,
		Location:         param.In,
		AllowEmptyValue:  (param.In == "query" || param.In == "formData") && param.AllowEmptyValue,
		Extensions:       param.Extensions,
	}

	if param.In == "body" {
		// Process parameters declared in body (i.e. have a Schema)
		var schema GenSchema
		var named bool
		rslv := resolver
		sch := param.Schema
		if sch.Ref.String() != "" && !sch.Ref.HasFragmentOnly {
			// This section attempts to resolve external $ref (e.g. which are not only a fragment)
			// TODO: this is currently unused as always resolved beforehand by flattening/expanding the spec.
			// We should guard the generator against complex $ref, such as external, or pointing to anonymous places.
			ss, err := spec.ResolveRefWithBase(b.Doc.Spec(), &sch.Ref, nil)
			if err != nil {
				return GenParameter{}, err
			}
			sch = ss
			// this is a named schema (i.e. with an external $ref)
			named = true
			rslv = resolver.NewWithModelName(b.Operation.ID + "ParamsBody")
		}

		// resolve schema model
		sc := schemaGenContext{
			Path:             res.Path,
			Name:             b.Operation.ID + "ParamsBody",
			Receiver:         res.ReceiverName,
			ValueExpr:        res.ReceiverName,
			IndexVar:         res.IndexVar,
			Schema:           *sch,
			Required:         param.Required,
			TypeResolver:     rslv,
			Named:            named,
			IncludeModel:     true,
			IncludeValidator: true,
			ExtraSchemas:     make(map[string]GenSchema),
		}
		br, bs := b.saveResolveContext(rslv, sch)
		if err := sc.makeGenSchema(); err != nil {
			return GenParameter{}, err
		}

		if !strings.HasPrefix(sch.Ref.String(), "#/definitions") && len(sc.ExtraSchemas) > 0 {
			// when some ExtraSchemas are produced from something else than a definition,
			// this indicates we are not running in fully flattened mode and we need to render
			// some ExtraSchemas in the operation's package.
			// We need to rebuild the schema with a new type resolver to reflect this change in the
			// models package.

			// restore resolving state before previous call to makeGenSchema()
			rslv = br
			sc.Schema = *bs

			pg := sc.shallowClone()
			pg.TypeResolver = newTypeResolver("", rslv.Doc).withKeepDefinitionsPackage(resolver.ModelsPackage)
			pg.ExtraSchemas = make(map[string]GenSchema, len(sc.ExtraSchemas))

			if err := pg.makeGenSchema(); err != nil {
				return GenParameter{}, err
			}
			// lift nested extra schemas (inlined types)
			if b.ExtraSchemas == nil {
				b.ExtraSchemas = make(map[string]GenSchema, len(pg.ExtraSchemas))
			}
			for _, v := range pg.ExtraSchemas {
				vv := v
				vv.GoType = vv.Name
				vv.IsAnonymous = false
				if !v.IsStream {
					b.ExtraSchemas[vv.Name] = vv
				}
			}
			schema = pg.GenSchema
		} else {
			schema = sc.GenSchema
		}

		if named {
			// TODO: this is currently unused as always resolved beforehand by flattening/expanding the spec.
			if b.ExtraSchemas == nil {
				b.ExtraSchemas = make(map[string]GenSchema)
			}
			b.ExtraSchemas[b.Operation.ID+"ParamsBody"] = schema
		}

		if schema.IsAnonymous {
			// A generated name for anonymous parameter in body

			schema.Name = swag.ToGoName(b.Operation.ID + " Body")
			nm := schema.Name
			schema.GoType = nm
			schema.IsAnonymous = false
			if len(schema.Properties) > 0 {
				if b.ExtraSchemas == nil {
					b.ExtraSchemas = make(map[string]GenSchema)
				}
				b.ExtraSchemas[nm] = schema
			}
			prevSchema := schema
			schema = GenSchema{}
			schema.IsAnonymous = false
			schema.GoType = nm
			schema.SwaggerType = nm

			if len(prevSchema.Properties) == 0 {
				schema.GoType = iface
			}
			schema.IsComplexObject = true
			schema.IsInterface = len(schema.Properties) == 0
		}
		res.Schema = &schema

		// clone the .Items schema structure as a .GenItems structure
		// for compatibility with simple param templates
		it := res.Schema.Items
		items := new(GenItems)
		if it != nil {
			var prev *GenItems
			next := items
			next.Name = res.Name + " " + res.Schema.IndexVar
			next.IndexVar = res.Schema.IndexVar + "i"
			next.ValueExpression = swag.ToVarName(res.Name + "I")
			next.Path = "fmt.Sprintf(\"%s.%v\", " + res.Path + ", " + res.IndexVar + ")"
			next.Location = "body"
			for it != nil {
				next.resolvedType = it.resolvedType
				next.sharedValidations = it.sharedValidations
				next.Formatter = stringFormatters[it.SwaggerFormat]
				next.Converter = stringConverters[res.GoType]
				next.Parent = prev
				_, next.IsCustomFormatter = customFormatters[it.GoType]
				next.IsCustomFormatter = next.IsCustomFormatter && !it.IsStream

				// special instruction to avoid using CollectionFormat for body params
				next.SkipParse = true

				if prev != nil {
					next.Name = prev.Name + prev.IndexVar
					next.IndexVar = prev.IndexVar + "i"
					next.ValueExpression = swag.ToVarName(prev.ValueExpression + "I")
					next.Path = "fmt.Sprintf(\"%s.%v\", " + prev.Path + ", " + prev.IndexVar + ")"
					prev.Child = next
				}

				// found a complex or aliased thing
				// hide details from the aliased type and stop recursing
				if next.IsAliased || next.IsComplexObject {
					next.IsArray = false
					next.IsCustomFormatter = false
					if !(next.IsInterface || next.IsStream) {
						next.HasValidations = true
					}
					next.IsComplexObject = true
					next.IsAliased = true
					break
				}
				prev = next
				next = new(GenItems)
				it = it.Items
			}
			// propagate HasValidations
			var propag func(child *GenItems) bool
			propag = func(child *GenItems) bool {
				if child == nil {
					return false
				}
				child.HasValidations = child.HasValidations || propag(child.Child)
				return child.HasValidations
			}
			items.HasValidations = propag(items)
			schema.HasValidations = schema.HasValidations || items.HasValidations
		}

		// templates assume at least one .Child != nil
		res.Child = items
		res.resolvedType = schema.resolvedType

		// simple and schema views share the same validations
		res.sharedValidations = schema.sharedValidations
		res.ZeroValue = schema.Zero()
	} else {
		// Process parameters declared in other inputs: path, query, header (SimpleSchema)
		res.resolvedType = simpleResolvedType(param.Type, param.Format, param.Items)
		res.sharedValidations = sharedValidationsFromSimple(param.CommonValidations, param.Required)

		res.ZeroValue = res.resolvedType.Zero()

		hasChildValidations := false
		if param.Items != nil {
			// Follow Items definition for array parameters
			pi, err := b.MakeParameterItem(receiver, param.Name+" "+res.IndexVar, res.IndexVar+"i", "fmt.Sprintf(\"%s.%v\", "+res.Path+", "+res.IndexVar+")", res.Name+"I", param.In, resolver, param.Items, nil)
			if err != nil {
				return GenParameter{}, err
			}
			res.Child = &pi
			// Propagates HasValidations from from child array
			hasChildValidations = pi.HasValidations
		}
		res.IsNullable = !param.Required && !param.AllowEmptyValue
		res.HasValidations, res.HasSliceValidations = b.HasValidations(param.CommonValidations, res.resolvedType)
		res.HasValidations = res.HasValidations || hasChildValidations
	}

	res.Converter = stringConverters[res.GoType]
	res.Formatter = stringFormatters[res.GoType]

	b.setBodyParamValidation(&res)

	return res, nil
}

func (b *codeGenOpBuilder) setBodyParamValidation(p *GenParameter) {
	// determine validation strategy for body param
	if p.IsBodyParam() {
		var hasSimpleBodyParams, hasSimpleBodyItems, hasModelBodyParams, hasModelBodyItems bool
		s := p.Schema
		if s != nil {
			doNot := s.IsInterface || s.IsStream
			// composition of primitive fields must be properly identified: hack this through
			_, isPrimitive := primitives[s.GoType]
			_, isFormatter := customFormatters[s.GoType]
			isComposedPrimitive := s.IsPrimitive && !(isPrimitive || isFormatter)

			hasSimpleBodyParams = !s.IsComplexObject && !s.IsAliased && !isComposedPrimitive && !doNot
			hasModelBodyParams = (s.IsComplexObject || s.IsAliased || isComposedPrimitive) && !doNot

			if s.IsArray && s.Items != nil {
				it := s.Items
				doNot := it.IsInterface || it.IsStream
				hasSimpleBodyItems = !it.IsComplexObject && !(it.IsAliased || doNot)
				hasModelBodyItems = (it.IsComplexObject || it.IsAliased) && !doNot
			}
		}
		// set validation strategy for body param
		p.HasSimpleBodyParams = hasSimpleBodyParams
		p.HasSimpleBodyItems = hasSimpleBodyItems
		p.HasModelBodyParams = hasModelBodyParams
		p.HasModelBodyItems = hasModelBodyItems
	}

}

// makeSecuritySchemes produces a sorted list of security schemes for this operation
func (b *codeGenOpBuilder) makeSecuritySchemes(receiver string) GenSecuritySchemes {
	return gatherSecuritySchemes(b.SecurityDefinitions, b.Name, b.Principal, receiver)
}

// makeSecurityRequirements produces a sorted list of security requirements for this operation.
// As for current, these requirements are not used by codegen (sec. requirement is determined at runtime).
// We keep the order of the slice from the original spec, but sort the inner slice which comes from a map,
// as well as the map of scopes.
func (b *codeGenOpBuilder) makeSecurityRequirements(receiver string) []GenSecurityRequirements {
	if b.Security == nil {
		// nil (default requirement) is different than [] (no requirement)
		return nil
	}

	securityRequirements := make([]GenSecurityRequirements, 0, len(b.Security))
	for _, req := range b.Security {
		jointReq := make(GenSecurityRequirements, 0, len(req))
		for _, j := range req {
			scopes := j.Scopes
			sort.Strings(scopes)
			jointReq = append(jointReq, GenSecurityRequirement{
				Name:   j.Name,
				Scopes: scopes,
			})
		}
		// sort joint requirements (come from a map in spec)
		sort.Sort(jointReq)
		securityRequirements = append(securityRequirements, jointReq)
	}
	return securityRequirements
}

// cloneSchema returns a deep copy of a schema
func (b *codeGenOpBuilder) cloneSchema(schema *spec.Schema) *spec.Schema {
	savedSchema := &spec.Schema{}
	schemaRep, _ := json.Marshal(schema)
	_ = json.Unmarshal(schemaRep, savedSchema)
	return savedSchema
}

// saveResolveContext keeps a copy of known definitions and schema to properly roll back on a makeGenSchema() call
// This uses a deep clone the spec document to construct a type resolver which knows about definitions when the making of this operation started,
// and only these definitions. We are not interested in the "original spec", but in the already transformed spec.
func (b *codeGenOpBuilder) saveResolveContext(resolver *typeResolver, schema *spec.Schema) (*typeResolver, *spec.Schema) {
	rslv := newTypeResolver(resolver.ModelsPackage, b.Doc.Pristine())

	return rslv, b.cloneSchema(schema)
}

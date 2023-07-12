package soda

import (
	"context"
	"log"
	"net/http"
	"reflect"
	"strconv"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/gofiber/fiber/v2"
)

// OperationBuilder is a builder for a single operation.
type OperationBuilder struct {
	input     reflect.Type
	inputBody reflect.Type

	soda      *Soda
	operation *openapi3.Operation

	path               string
	method             string
	inputBodyMediaType string
	inputBodyField     string

	handlers []fiber.Handler
}

// SetSummary sets the operation-id.
func (op *OperationBuilder) SetOperationID(id string) *OperationBuilder {
	op.operation.OperationID = id
	return op
}

// SetSummary sets the operation summary.
func (op *OperationBuilder) SetSummary(summary string) *OperationBuilder {
	op.operation.Summary = summary
	return op
}

// SetDescription sets the operation description.
func (op *OperationBuilder) SetDescription(desc string) *OperationBuilder {
	op.operation.Description = desc
	return op
}

// AddTags add tags to the operation.
func (op *OperationBuilder) AddTags(tags ...string) *OperationBuilder {
	op.operation.Tags = append(op.operation.Tags, tags...)
	for _, tag := range tags {
		if t := op.soda.generator.spec.Tags.Get(tag); t == nil {
			op.soda.generator.spec.Tags = append(op.soda.generator.spec.Tags, &openapi3.Tag{Name: tag})
		}
	}
	return op
}

// SetDeprecated marks the operation as deprecated.
func (op *OperationBuilder) SetDeprecated(deprecated bool) *OperationBuilder {
	op.operation.Deprecated = deprecated
	return op
}

// SetInput sets the input for this operation.
// The input must be a pointer to a struct.
// If the struct has a field with the `body:"<media type>"` tag, that field is used for the request body.
// Otherwise, the struct is used for parameters.
func (op *OperationBuilder) SetInput(input interface{}) *OperationBuilder {
	inputType := reflect.TypeOf(input)
	// the input type should be a struct or pointer to a struct
	for inputType.Kind() == reflect.Ptr {
		inputType = inputType.Elem()
	}
	if inputType.Kind() != reflect.Struct {
		panic("input must be a pointer to a struct")
	}

	op.input = inputType
	for i := 0; i < inputType.NumField(); i++ {
		if body := inputType.Field(i); body.Tag.Get("body") != "" {
			op.inputBody = body.Type
			op.inputBodyMediaType = body.Tag.Get("body")
			op.inputBodyField = body.Name
			break
		}
	}
	op.operation.Parameters = op.soda.generator.GenerateParameters(inputType)
	if op.inputBodyField != "" {
		op.operation.RequestBody = op.soda.generator.GenerateRequestBody(op.operation.OperationID, op.inputBodyMediaType, op.inputBody)
	}
	return op
}

// AddJWTSecurity adds JWT authentication to this operation with the given validators.
func (op *OperationBuilder) AddSecurity(name string, scheme *openapi3.SecurityScheme) *OperationBuilder {
	// add the JWT security scheme to the spec if it doesn't already exist
	if _, ok := op.soda.generator.spec.Components.SecuritySchemes[name]; !ok {
		op.soda.generator.spec.Components.SecuritySchemes[name] = &openapi3.SecuritySchemeRef{Value: scheme}
	}

	// add the security scheme to the operation
	if op.operation.Security == nil {
		op.operation.Security = openapi3.NewSecurityRequirements()
	}
	op.operation.Security.With(openapi3.NewSecurityRequirement().Authenticate(name))
	return op
}

// AddJSONResponse adds a JSON response to the operation's responses.
// If model is not nil, a JSON response is generated for the model type.
// If model is nil, a JSON response is generated with no schema.
func (op *OperationBuilder) AddJSONResponse(status int, model interface{}) *OperationBuilder {
	if len(op.operation.Responses) == 0 {
		op.operation.Responses = make(openapi3.Responses)
	}
	if model == nil {
		op.operation.AddResponse(status, openapi3.NewResponse().WithDescription(http.StatusText(status)))
		return op
	}
	ref := op.soda.generator.GenerateResponse(op.operation.OperationID, status, reflect.TypeOf(model), "json")
	op.operation.Responses[strconv.Itoa(status)] = ref
	return op
}

func (op *OperationBuilder) OK() *OperationBuilder {
	// Add default response if not exists
	if op.operation.Responses == nil {
		op.operation.AddResponse(0, openapi3.NewResponse().WithDescription("OK"))
	}

	// Validate the operation
	if err := op.operation.Validate(context.TODO()); err != nil {
		log.Fatalln(err)
	}

	// Add operation to the spec
	op.soda.generator.spec.AddOperation(fixPath(op.path), op.method, op.operation)

	// Validate the spec
	if err := op.soda.generator.spec.Validate(context.TODO()); err != nil {
		log.Fatalln(err)
	}

	// Add handler
	op.handlers = append([]fiber.Handler{op.bindInput()}, op.handlers...)

	// Add route to the fiber app
	op.soda.Fiber.Add(op.method, op.path, op.handlers...)

	return op
}

// bindInput binds the request body to the input struct.
func (op *OperationBuilder) bindInput() fiber.Handler {
	return func(c *fiber.Ctx) error {
		if op.input == nil {
			return c.Next()
		}

		// create a new instance of the input struct
		input := reflect.New(op.input).Interface()

		// parse the request parameters
		for _, parser := range parameterParsers {
			if err := parser(c, input); err != nil {
				return err
			}
		}

		// parse the request body
		if op.inputBodyField != "" {
			body := reflect.New(op.inputBody).Interface()
			if err := c.BodyParser(body); err != nil {
				return err
			}
			reflect.ValueOf(input).Elem().FieldByName(op.inputBodyField).Set(reflect.ValueOf(body).Elem())
		}

		// if the validator is not nil then validate the input struct
		if op.soda.validator != nil {
			if err := op.soda.validator.Struct(input); err != nil {
				return err
			}
		}

		// if the input implements the CustomizeValidate interface then call the Validate function
		if v, ok := input.(customizeValidate); ok {
			if err := v.Validate(); err != nil {
				return err
			}
		}
		// if the input implements the CustomizeValidateCtx interface then call the Validate function
		if v, ok := input.(customizeValidateCtx); ok {
			if err := v.Validate(c.Context()); err != nil {
				return err
			}
		}

		// add the input struct to the context
		c.Locals(KeyInput, input)
		return c.Next()
	}
}

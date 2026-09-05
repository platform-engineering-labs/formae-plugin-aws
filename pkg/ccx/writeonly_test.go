// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package ccx

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	cctypes "github.com/aws/aws-sdk-go-v2/service/cloudcontrol/types"
	smithy "github.com/aws/smithy-go"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/platform-engineering-labs/formae-plugin-aws/pkg/ptr"
)

// validationError builds a CloudControl ValidationException in the shape the
// SDK delivers it: a generic API error inside an operation error. CloudControl
// declares no typed ValidationException, which is why this is not a cctypes
// value and why such an error is not classified into a ProgressResult.
func validationError(message string) error {
	return &smithy.OperationError{
		ServiceID:     "CloudControl",
		OperationName: "UpdateResource",
		Err:           &smithy.GenericAPIError{Code: "ValidationException", Message: message},
	}
}

// writeOnlyRejection is what CloudControl returns when a patch uses an
// operation other than 'add' on a writeOnly property.
func writeOnlyRejection(properties string) error {
	return validationError("Invalid patch update: writeOnlyProperties [" + properties + "] can only be updated using 'add' operation")
}

func TestWriteOnlyPathsFromError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want []string
	}{
		{
			name: "reads the single path CloudControl names",
			err:  writeOnlyRejection("/Code/ZipFile"),
			want: []string{"/Code/ZipFile"},
		},
		{
			name: "reads every path of a multi-property rejection",
			err:  writeOnlyRejection("/Code/ZipFile, /SnapStart/ApplyOn"),
			want: []string{"/Code/ZipFile", "/SnapStart/ApplyOn"},
		},
		{
			name: "reads the paths even when the surrounding prose differs",
			err:  validationError("writeOnlyProperties [/Code/ZipFile] must be supplied with 'add'"),
			want: []string{"/Code/ZipFile"},
		},
		{
			name: "ignores an unrelated validation failure",
			err:  validationError("Invalid patch update: operation 'replace' at /Tags is not supported"),
			want: nil,
		},
		{
			name: "ignores an unrelated error",
			err:  errors.New("connection reset by peer"),
			want: nil,
		},
		{
			name: "ignores a nil error",
			err:  nil,
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, writeOnlyPathsFromError(tt.err))
		})
	}
}

// TestWriteOnlyPathsFromError_LiveMessage pins the parser against the message
// CloudControl actually returns, captured verbatim from an UpdateResource call.
// The message shape is the one assumption this design rests on, so it is worth
// asserting against the real thing rather than only against a constructed one.
func TestWriteOnlyPathsFromError_LiveMessage(t *testing.T) {
	err := errors.New("operation error CloudControl: UpdateResource, https response error " +
		"StatusCode: 400, RequestID: 0a1b, api error ValidationException: Invalid patch update: " +
		"writeOnlyProperties [/SecretString] can only be updated using 'add' operation")

	require.Equal(t, []string{"/SecretString"}, writeOnlyPathsFromError(err))
}

func TestTransformWriteOnlyPatch(t *testing.T) {
	tests := []struct {
		name      string
		writeOnly []string
		patch     string
		want      string
	}{
		{
			name:      "rewrites replace to add on a named property",
			writeOnly: []string{"/Code/ZipFile"},
			patch:     `[{"op":"replace","path":"/Code/ZipFile","value":"print(1)"}]`,
			want:      `[{"op":"add","path":"/Code/ZipFile","value":"print(1)"}]`,
		},
		{
			name:      "leaves a property CloudControl did not name alone",
			writeOnly: []string{"/Code/ZipFile"},
			patch:     `[{"op":"replace","path":"/Timeout","value":30}]`,
			want:      `[{"op":"replace","path":"/Timeout","value":30}]`,
		},
		{
			name:      "rewrites only the named operations of a mixed patch",
			writeOnly: []string{"/Code/ZipFile"},
			patch:     `[{"op":"replace","path":"/Timeout","value":30},{"op":"replace","path":"/Code/ZipFile","value":"print(1)"}]`,
			want:      `[{"op":"replace","path":"/Timeout","value":30},{"op":"add","path":"/Code/ZipFile","value":"print(1)"}]`,
		},
		{
			name:      "does not match a property that merely shares a prefix",
			writeOnly: []string{"/Code"},
			patch:     `[{"op":"replace","path":"/CodeSigningConfigArn","value":"arn:aws:lambda:::x"}]`,
			want:      `[{"op":"replace","path":"/CodeSigningConfigArn","value":"arn:aws:lambda:::x"}]`,
		},
		{
			name:      "leaves add operations alone",
			writeOnly: []string{"/Code/ZipFile"},
			patch:     `[{"op":"add","path":"/Code/ZipFile","value":"print(1)"}]`,
			want:      `[{"op":"add","path":"/Code/ZipFile","value":"print(1)"}]`,
		},
		{
			name:      "leaves remove operations alone",
			writeOnly: []string{"/Code/ZipFile"},
			patch:     `[{"op":"remove","path":"/Code/ZipFile"}]`,
			want:      `[{"op":"remove","path":"/Code/ZipFile"}]`,
		},
		{
			name:      "returns the patch unchanged when no property was named",
			writeOnly: nil,
			patch:     `[{"op":"replace","path":"/Description","value":"x"}]`,
			want:      `[{"op":"replace","path":"/Description","value":"x"}]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := transformWriteOnlyPatch(tt.patch, tt.writeOnly)
			require.NoError(t, err)
			require.JSONEq(t, tt.want, got)
		})
	}
}

func TestTransformWriteOnlyPatch_MalformedPatchIsReturnedUnchanged(t *testing.T) {
	got, err := transformWriteOnlyPatch(`not json`, []string{"/Code/ZipFile"})
	require.Error(t, err)
	require.Equal(t, `not json`, got)
}

// stubExistenceCheck wires the GetResource call UpdateResource makes before it
// sends the patch.
func stubExistenceCheck(mockAPI *mockCloudControlAPI, nativeID string) {
	mockAPI.On("GetResource", mock.Anything, mock.Anything).Return(&cloudcontrol.GetResourceOutput{
		ResourceDescription: &cctypes.ResourceDescription{
			Identifier: ptr.Of(nativeID),
			Properties: ptr.Of(`{}`),
		},
	}, nil)
}

func acceptedUpdate(nativeID string) *cloudcontrol.UpdateResourceOutput {
	return &cloudcontrol.UpdateResourceOutput{
		ProgressEvent: &cctypes.ProgressEvent{
			OperationStatus: cctypes.OperationStatusInProgress,
			RequestToken:    ptr.Of("req-token"),
			Identifier:      ptr.Of(nativeID),
		},
	}
}

// capturePatch records the patch document of a matched call. It hooks Run
// rather than a matcher: testify evaluates a matcher once per candidate
// expectation, so capturing there records calls that never happened.
func capturePatch(into *[]string) func(mock.Arguments) {
	return func(args mock.Arguments) {
		input := args.Get(1).(*cloudcontrol.UpdateResourceInput)
		*into = append(*into, *input.PatchDocument)
	}
}

func TestUpdateResource_ResendsWithAddWhenCloudControlNamesAWriteOnlyProperty(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	client := &Client{api: mockAPI}
	stubExistenceCheck(mockAPI, "fn")

	var transmitted []string
	mockAPI.On("UpdateResource", mock.Anything, mock.Anything).Run(capturePatch(&transmitted)).
		Return((*cloudcontrol.UpdateResourceOutput)(nil), writeOnlyRejection("/Code/ZipFile")).Once()
	mockAPI.On("UpdateResource", mock.Anything, mock.Anything).Run(capturePatch(&transmitted)).
		Return(acceptedUpdate("fn"), nil).Once()

	result, err := client.UpdateResource(context.Background(), &resource.UpdateRequest{
		NativeID:      "fn",
		ResourceType:  "AWS::Lambda::Function",
		PatchDocument: ptr.Of(`[{"op":"replace","path":"/Code/ZipFile","value":"print(1)"}]`),
	})

	require.NoError(t, err)
	require.Equal(t, resource.OperationStatusInProgress, result.ProgressResult.OperationStatus)
	require.Len(t, transmitted, 2)
	require.JSONEq(t, `[{"op":"replace","path":"/Code/ZipFile","value":"print(1)"}]`, transmitted[0])
	require.JSONEq(t, `[{"op":"add","path":"/Code/ZipFile","value":"print(1)"}]`, transmitted[1])
}

func TestUpdateResource_ResendsForSecretString(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	client := &Client{api: mockAPI}
	stubExistenceCheck(mockAPI, "secret")

	var transmitted []string
	mockAPI.On("UpdateResource", mock.Anything, mock.Anything).Run(capturePatch(&transmitted)).
		Return((*cloudcontrol.UpdateResourceOutput)(nil), writeOnlyRejection("/SecretString")).Once()
	mockAPI.On("UpdateResource", mock.Anything, mock.Anything).Run(capturePatch(&transmitted)).
		Return(acceptedUpdate("secret"), nil).Once()

	_, err := client.UpdateResource(context.Background(), &resource.UpdateRequest{
		NativeID:      "secret",
		ResourceType:  "AWS::SecretsManager::Secret",
		PatchDocument: ptr.Of(`[{"op":"replace","path":"/SecretString","value":"s3cr3t"}]`),
	})

	require.NoError(t, err)
	require.Len(t, transmitted, 2)
	require.JSONEq(t, `[{"op":"add","path":"/SecretString","value":"s3cr3t"}]`, transmitted[1])
}

func TestUpdateResource_ConvergesWhenCloudControlNamesOnePropertyAtATime(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	client := &Client{api: mockAPI}
	stubExistenceCheck(mockAPI, "fn")

	var transmitted []string
	mockAPI.On("UpdateResource", mock.Anything, mock.Anything).Run(capturePatch(&transmitted)).
		Return((*cloudcontrol.UpdateResourceOutput)(nil), writeOnlyRejection("/Code/ZipFile")).Once()
	mockAPI.On("UpdateResource", mock.Anything, mock.Anything).Run(capturePatch(&transmitted)).
		Return((*cloudcontrol.UpdateResourceOutput)(nil), writeOnlyRejection("/SnapStart")).Once()
	mockAPI.On("UpdateResource", mock.Anything, mock.Anything).Run(capturePatch(&transmitted)).
		Return(acceptedUpdate("fn"), nil).Once()

	_, err := client.UpdateResource(context.Background(), &resource.UpdateRequest{
		NativeID:     "fn",
		ResourceType: "AWS::Lambda::Function",
		PatchDocument: ptr.Of(`[{"op":"replace","path":"/Code/ZipFile","value":"print(1)"},` +
			`{"op":"replace","path":"/SnapStart","value":{"ApplyOn":"None"}}]`),
	})

	require.NoError(t, err)
	require.Len(t, transmitted, 3)
	require.JSONEq(t, `[{"op":"add","path":"/Code/ZipFile","value":"print(1)"},`+
		`{"op":"add","path":"/SnapStart","value":{"ApplyOn":"None"}}]`, transmitted[2])
}

func TestUpdateResource_DoesNotResendWhenTheRewriteChangesNothing(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	client := &Client{api: mockAPI}
	stubExistenceCheck(mockAPI, "fn")

	// CloudControl names a property the patch carries no replace for, so there
	// is nothing to rewrite and a resend would repeat the same request.
	mockAPI.On("UpdateResource", mock.Anything, mock.Anything).
		Return((*cloudcontrol.UpdateResourceOutput)(nil), writeOnlyRejection("/Code/S3Bucket"))

	_, err := client.UpdateResource(context.Background(), &resource.UpdateRequest{
		NativeID:      "fn",
		ResourceType:  "AWS::Lambda::Function",
		PatchDocument: ptr.Of(`[{"op":"replace","path":"/Code/ZipFile","value":"print(1)"}]`),
	})

	require.Error(t, err)
	mockAPI.AssertNumberOfCalls(t, "UpdateResource", 1)
}

func TestUpdateResource_LeavesAnUnrelatedFailureAlone(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	client := &Client{api: mockAPI}
	stubExistenceCheck(mockAPI, "fn")

	mockAPI.On("UpdateResource", mock.Anything, mock.Anything).
		Return((*cloudcontrol.UpdateResourceOutput)(nil), validationError("Invalid patch update: unknown property /Nope"))

	_, err := client.UpdateResource(context.Background(), &resource.UpdateRequest{
		NativeID:      "fn",
		ResourceType:  "AWS::Lambda::Function",
		PatchDocument: ptr.Of(`[{"op":"replace","path":"/Code/ZipFile","value":"print(1)"}]`),
	})

	require.Error(t, err)
	mockAPI.AssertNumberOfCalls(t, "UpdateResource", 1)
}

func TestMaxWriteOnlyResends(t *testing.T) {
	tests := []struct {
		name  string
		patch string
		want  int
	}{
		{
			name:  "one resend per replace operation",
			patch: `[{"op":"replace","path":"/A","value":1},{"op":"replace","path":"/B","value":2}]`,
			want:  2,
		},
		{
			name:  "add and remove operations buy no resends",
			patch: `[{"op":"add","path":"/A","value":1},{"op":"remove","path":"/B"}]`,
			want:  0,
		},
		{
			name:  "an unparseable patch buys no resends",
			patch: `not json`,
			want:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, maxWriteOnlyResends(tt.patch))
		})
	}
}

func TestUpdateResource_ConvergesOnAPatchWithMoreWriteOnlyPropertiesThanAFixedCap(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	client := &Client{api: mockAPI}
	stubExistenceCheck(mockAPI, "fn")

	// Six writeOnly properties named one per rejection. AWS::Lambda::Function
	// carries ten and AWS::RDS::DBInstance sixteen, so the resend budget has to
	// come from the patch rather than from a constant.
	paths := []string{"/A", "/B", "/C", "/D", "/E", "/F"}
	var transmitted []string
	for _, path := range paths {
		mockAPI.On("UpdateResource", mock.Anything, mock.Anything).Run(capturePatch(&transmitted)).
			Return((*cloudcontrol.UpdateResourceOutput)(nil), writeOnlyRejection(path)).Once()
	}
	mockAPI.On("UpdateResource", mock.Anything, mock.Anything).Run(capturePatch(&transmitted)).
		Return(acceptedUpdate("fn"), nil).Once()

	ops := make([]string, 0, len(paths))
	for i, path := range paths {
		ops = append(ops, fmt.Sprintf(`{"op":"replace","path":%q,"value":%d}`, path, i))
	}

	_, err := client.UpdateResource(context.Background(), &resource.UpdateRequest{
		NativeID:      "fn",
		ResourceType:  "AWS::Lambda::Function",
		PatchDocument: ptr.Of("[" + strings.Join(ops, ",") + "]"),
	})

	require.NoError(t, err)
	require.Len(t, transmitted, len(paths)+1)
	for _, path := range paths {
		require.Contains(t, transmitted[len(transmitted)-1], `{"op":"add","path":"`+path+`"`)
	}
}

func TestUpdateResource_ResendsAreBoundedByTheReplaceOperationsInThePatch(t *testing.T) {
	mockAPI := new(mockCloudControlAPI)
	client := &Client{api: mockAPI}
	stubExistenceCheck(mockAPI, "fn")

	// CloudControl keeps naming properties that are not in the patch, so no
	// rewrite ever changes it and the very first resend attempt is refused.
	var transmitted []string
	mockAPI.On("UpdateResource", mock.Anything, mock.Anything).Run(capturePatch(&transmitted)).
		Return((*cloudcontrol.UpdateResourceOutput)(nil), writeOnlyRejection("/NotInThePatch"))

	_, err := client.UpdateResource(context.Background(), &resource.UpdateRequest{
		NativeID:      "fn",
		ResourceType:  "AWS::Lambda::Function",
		PatchDocument: ptr.Of(`[{"op":"replace","path":"/A","value":1},{"op":"replace","path":"/B","value":2}]`),
	})

	require.Error(t, err)
	require.Len(t, transmitted, 1)
}

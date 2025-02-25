/*
Copyright 2021 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package composition

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/crossplane/crossplane-runtime/pkg/resource/fake"
	"github.com/crossplane/crossplane-runtime/pkg/test"
	v1 "github.com/crossplane/crossplane/apis/apiextensions/v1"
	"github.com/crossplane/crossplane/apis/apiextensions/v1alpha1"
)

func TestReconcile(t *testing.T) {
	errBoom := errors.New("boom")
	ctrl := true

	comp := &v1.Composition{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cool-composition",
			UID:  types.UID("no-you-uid"),
		},
	}

	// Not owned by the above composition.
	rev1 := &v1alpha1.CompositionRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name: comp.GetName() + "-1",
		},
		Spec: v1alpha1.CompositionRevisionSpec{Revision: 1},
	}

	// Owned by the above composition, but with an 'older' hash. The status
	// indicates it is the current version, and thus should be updated.
	rev2 := &v1alpha1.CompositionRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name: comp.GetName() + "-2",
			OwnerReferences: []metav1.OwnerReference{{
				UID:        comp.GetUID(),
				Controller: &ctrl,
			}},
			Labels: map[string]string{
				v1alpha1.LabelCompositionSpecHash: "some-older-hash",
			},
		},
		Spec: v1alpha1.CompositionRevisionSpec{Revision: 2},
		Status: v1alpha1.CompositionRevisionStatus{
			ConditionedStatus: xpv1.ConditionedStatus{
				Conditions: []xpv1.Condition{v1alpha1.CompositionSpecMatches()},
			},
		},
	}

	// Owned by the above composition, with a current hash. The status
	// indicates it is not the current revision, and thus should be updated.
	rev3 := &v1alpha1.CompositionRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name: comp.GetName() + "-3",
			OwnerReferences: []metav1.OwnerReference{{
				UID:        comp.GetUID(),
				Controller: &ctrl,
			}},
			Labels: map[string]string{
				v1alpha1.LabelCompositionSpecHash: comp.Spec.Hash(),
			},
		},
		Spec: v1alpha1.CompositionRevisionSpec{Revision: 3},
		Status: v1alpha1.CompositionRevisionStatus{
			ConditionedStatus: xpv1.ConditionedStatus{
				Conditions: []xpv1.Condition{v1alpha1.CompositionSpecDiffers()},
			},
		},
	}

	// Owned by the above composition, with a current hash. The status
	// indicates it is the current revision, and thus should not be updated.
	rev4 := &v1alpha1.CompositionRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name: comp.GetName() + "-4",
			OwnerReferences: []metav1.OwnerReference{{
				UID:        comp.GetUID(),
				Controller: &ctrl,
			}},
			Labels: map[string]string{
				v1alpha1.LabelCompositionSpecHash: comp.Spec.Hash(),
			},
		},
		Spec: v1alpha1.CompositionRevisionSpec{Revision: 3},
		Status: v1alpha1.CompositionRevisionStatus{
			ConditionedStatus: xpv1.ConditionedStatus{
				Conditions: []xpv1.Condition{v1alpha1.CompositionSpecMatches()},
			},
		},
	}

	type args struct {
		mgr  manager.Manager
		opts []ReconcilerOption
	}
	type want struct {
		r   reconcile.Result
		err error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"CompositionNotFound": {
			reason: "We should not return an error if the Composition was not found.",
			args: args{
				mgr: &fake.Manager{
					Client: &test.MockClient{
						MockGet: test.NewMockGetFn(kerrors.NewNotFound(schema.GroupResource{}, "")),
					},
				},
			},
			want: want{
				r: reconcile.Result{},
			},
		},
		"GetCompositionError": {
			reason: "We should return any other error encountered while getting a Composition.",
			args: args{
				mgr: &fake.Manager{
					Client: &test.MockClient{
						MockGet: test.NewMockGetFn(errBoom),
					},
				},
			},
			want: want{
				err: errors.Wrap(errBoom, errGet),
			},
		},
		"CompositionDeleted": {
			reason: "We should return without error if the Composition exists but is being deleted.",
			args: args{
				mgr: &fake.Manager{
					Client: &test.MockClient{
						MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
							now := metav1.Now()
							*obj.(*v1.Composition) = v1.Composition{ObjectMeta: metav1.ObjectMeta{DeletionTimestamp: &now}}
							return nil
						}),
					},
				},
			},
			want: want{
				r:   reconcile.Result{},
				err: nil,
			},
		},
		"ListCompositionRevisionsError": {
			reason: "We should return any error encountered while listing CompositionRevisions.",
			args: args{
				mgr: &fake.Manager{
					Client: &test.MockClient{
						MockGet:  test.NewMockGetFn(nil),
						MockList: test.NewMockListFn(errBoom),
					},
				},
			},
			want: want{
				err: errors.Wrap(errBoom, errListRevs),
			},
		},
		"SuccessfulNoOp": {
			reason: "We should not create a new CompositionRevision if one exists that matches the Composition's spec hash.",
			args: args{
				mgr: &fake.Manager{
					Client: &test.MockClient{
						MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
							*obj.(*v1.Composition) = *comp
							return nil
						}),
						MockList: test.NewMockListFn(nil, func(obj client.ObjectList) error {
							*obj.(*v1alpha1.CompositionRevisionList) = v1alpha1.CompositionRevisionList{
								Items: []v1alpha1.CompositionRevision{
									// Not controlled by the above composition.
									*rev1,

									// Controlled by the above composition with a current hash.
									// This indicates we don't need to create a new revision.
									// It does not need its status updated.
									*rev4,
								},
							}
							return nil
						}),
					},
				},
			},
			want: want{
				r:   reconcile.Result{},
				err: nil,
			},
		},
		"UpdateRevisionStatusError": {
			reason: "We should return any error encountered while updating a CompositionRevision's status.",
			args: args{
				mgr: &fake.Manager{
					Client: &test.MockClient{
						MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
							*obj.(*v1.Composition) = *comp
							return nil
						}),
						MockStatusUpdate: test.NewMockStatusUpdateFn(errBoom),
						MockList: test.NewMockListFn(nil, func(obj client.ObjectList) error {
							*obj.(*v1alpha1.CompositionRevisionList) = v1alpha1.CompositionRevisionList{
								Items: []v1alpha1.CompositionRevision{
									// Not controlled by the above composition.
									*rev1,

									// Controlled by the above composition with a current hash.
									// This indicates we don't need to create a new revision.
									// It does need its status updated to indicate that it it's
									// the current revision, though.
									*rev3,
								},
							}
							return nil
						}),
					},
				},
			},
			want: want{
				r:   reconcile.Result{},
				err: errors.Wrap(errBoom, errUpdateRevStatus),
			},
		},
		"CreateCompositionRevisionError": {
			reason: "We should return any error encountered while creating a CompositionRevision.",
			args: args{
				mgr: &fake.Manager{
					Client: &test.MockClient{
						MockGet:    test.NewMockGetFn(nil),
						MockList:   test.NewMockListFn(nil),
						MockCreate: test.NewMockCreateFn(errBoom),
					},
				},
			},
			want: want{
				err: errors.Wrap(errBoom, errCreateRev),
			},
		},
		"SuccessfulCreation": {
			reason: "We should increase the revision number by one when creating a new CompositionRevision.",
			args: args{
				mgr: &fake.Manager{
					Client: &test.MockClient{
						MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
							*obj.(*v1.Composition) = *comp
							return nil
						}),
						MockStatusUpdate: test.NewMockStatusUpdateFn(nil, func(obj client.Object) error {
							want := rev2.DeepCopy()
							want.Status.SetConditions(v1alpha1.CompositionSpecDiffers())

							if diff := cmp.Diff(want, obj, test.EquateConditions()); diff != "" {
								t.Errorf("Status().Update(...): -want, +got:\n%s", diff)
							}

							return nil
						}),
						MockList: test.NewMockListFn(nil, func(obj client.ObjectList) error {
							*obj.(*v1alpha1.CompositionRevisionList) = v1alpha1.CompositionRevisionList{
								Items: []v1alpha1.CompositionRevision{
									// Not controlled by the above composition.
									*rev1,

									// Controlled by the above composition, but with an older hash.
									// This indicates we need to create a new composition. The status
									// also needs updating to indicate this is not the currenr revision.
									*rev2,
								},
							}
							return nil
						}),
						MockCreate: test.NewMockCreateFn(nil, func(got client.Object) error {
							want := NewCompositionRevision(comp, rev2.Spec.Revision+1, comp.Spec.Hash())

							if diff := cmp.Diff(want, got); diff != "" {
								t.Errorf("Create(): -want, +got:\n%s", diff)
							}

							return nil
						}),
					},
				},
			},
			want: want{
				r:   reconcile.Result{},
				err: nil,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := NewReconciler(tc.args.mgr, tc.args.opts...)
			got, err := r.Reconcile(context.Background(), reconcile.Request{})

			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nr.Reconcile(...): -want error, +got error:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.r, got, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nr.Reconcile(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

// Package v1alpha1 scheme registration.
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
)

func NewScheme() (*runtime.Scheme, error) {
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := AddToScheme(s); err != nil {
		return nil, err
	}
	return s, nil
}

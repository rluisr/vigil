package utils

import "github.com/rluisr/vigil/model"

// ToInterfaceSlice converts a slice of SLO pointers to a slice of interface{}.
func ToInterfaceSlice(sl []*model.SLO) []interface{} {
	is := make([]interface{}, len(sl))
	for i, s := range sl {
		is[i] = s
	}
	return is
}

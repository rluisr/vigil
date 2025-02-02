package utils

import "github.com/rluisr/vigil/model"

func ToInterfaceSlice(sl []*model.SLO) []interface{} {
	is := make([]interface{}, len(sl))
	for i, s := range sl {
		is[i] = s
	}
	return is
}

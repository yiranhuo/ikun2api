package service

func containsInt64(values []int64, target int64) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}

// containsPositiveInt64 与 containsInt64 相同，但把 0 视为「未指定」并返回 false，
// 用于账号/分组这类 ID 一定为正数的排除名单判断。
func containsPositiveInt64(values []int64, target int64) bool {
	if target <= 0 {
		return false
	}
	return containsInt64(values, target)
}

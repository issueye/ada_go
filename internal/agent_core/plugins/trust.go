package plugins

// Origin 是插件的来源。信任级别不能超过来源允许的上限（09 §9.1、6.9）：
// builtin 只接受随包/官方目录；磁盘来源最高 verified（必须验签）；未验签/未知
// 来源按 third-party 处理。
type Origin string

const (
	// OriginOfficial 是随包/官方插件目录；只有它能承载 builtin。
	OriginOfficial Origin = "official"
	// OriginWorkspace 是 <workspace>/.adago/plugins（旧目录兼容）。
	OriginWorkspace Origin = "workspace"
	// OriginGlobal 是 ~/.adago/plugins（生产安装路径）。
	OriginGlobal Origin = "global"
	// OriginUnknown 是未标注来源（发现/预检的默认值），按不受信处理。
	OriginUnknown Origin = ""
)

// MaxTrust 返回该来源允许的最高信任级别。
func (o Origin) MaxTrust() Trust {
	switch o {
	case OriginOfficial:
		return TrustBuiltin
	case OriginWorkspace, OriginGlobal:
		return TrustVerified
	default:
		return TrustThirdParty
	}
}

// CapTrust 把声明的信任级别夹到来源允许的上限。
//
// verified 是否保留由发现层的验签决定：验签失败时调用方先把 trust 降为
// third-party，再走本函数（见 discovery.loadOne）。
func CapTrust(declared Trust, origin Origin) Trust {
	// builtin 不是靠签名升级的：磁盘来源不能把 builtin 转成 verified，
	// 否则一个自声明 builtin 的包会绕过验签变成 verified。
	if declared == TrustBuiltin && origin != OriginOfficial {
		return TrustThirdParty
	}
	max := origin.MaxTrust()
	if rank(declared) > rank(max) {
		return max
	}
	return declared
}

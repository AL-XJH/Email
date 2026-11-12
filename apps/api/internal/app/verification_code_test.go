package app

import "testing"

func TestExtractVerificationCode(t *testing.T) {
	cases := []struct {
		name    string
		subject string
		text    string
		want    string
	}{
		{
			name:    "中文验证码",
			subject: "您的临时 ChatGPT 验证码",
			text:    "输入此临时验证码以继续：857149 如果并非你本人尝试创建帐户，请忽略此电子邮件。",
			want:    "857149",
		},
		{
			name:    "Microsoft 一次性代码",
			subject: "你的一次性代码",
			text: "xjh@alxjh.cn，你好!\n\n我们已收到你要求获得 Microsoft 帐户所用的一次性代码的申请。\n\n" +
				"你的一次性代码为: 725432\n\n仅在官方网站或应用上输入此代码。",
			want: "725432",
		},
		{
			name:    "英文 single-use code",
			subject: "Your single-use code",
			text:    "Your single-use code is 638204.",
			want:    "638204",
		},
		{
			name:    "英文 verification code",
			subject: "Your GitHub verification code",
			text:    "Your one-time verification code is 638204. It expires in 15 minutes.",
			want:    "638204",
		},
		{
			name:    "passcode",
			subject: "Notion login code",
			text:    "Use passcode 5521 to finish signing in. If this wasn't you, ignore this email.",
			want:    "5521",
		},
		{
			name:    "订单号不当验证码",
			subject: "【京东】您的订单已签收",
			text:    "订单 2837461928 已由本人签收，感谢您在京东购物。",
			want:    "",
		},
		{
			name:    "车次号不当验证码",
			subject: "【铁路12306】购票成功通知",
			text:    "您已成功购买 11 月 12 日 G1024 次列车车票，北京南 08:00 开。",
			want:    "",
		},
		{
			name:    "关键词在主题、验证码在正文",
			subject: "验证码",
			text:    "您的验证码是 4402，10 分钟内有效。",
			want:    "4402",
		},
		{
			name:    "证书码这类字母数字混合",
			subject: "Your login code",
			text:    "Your one-time code: A1B2C3",
			want:    "A1B2C3",
		},
		{
			name:    "营销邮件整封信是一行，不能把显卡型号当验证码",
			subject: "DLSS 5 Comes to Hogwarts Legacy, War Thunder and Where Winds Meet",
			text: "Powered by 2X faster AI model - available on GeForce RTX and RTX Spark. " +
				"To redeem, download the Perplexity Windows app and enter promo code PERPLEXITYRTX in settings. " +
				"Dynamic Multi Frame Generation and Ray Reconstruction. The GeForce 8800 GTX turns 20! " +
				"Celebrate its gaming and CUDA legacy with GeForce Rewards.",
			want: "",
		},
		{
			name:    "主题里的时间戳不能抢走正文的验证码",
			subject: "您的临时验证码 222135",
			text:    "请输入以下验证码完成登录：739204 该验证码 10 分钟内有效。",
			want:    "739204",
		},
		{
			name:    "空内容",
			subject: "",
			text:    "",
			want:    "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractVerificationCode(tc.subject, tc.text); got != tc.want {
				t.Fatalf("extractVerificationCode(%q, %q) = %q, want %q", tc.subject, tc.text, got, tc.want)
			}
		})
	}
}

func TestVerificationCodeLongBodyStaysFast(t *testing.T) {
	long := make([]rune, 0, 20000)
	for i := 0; i < 20000; i++ {
		long = append(long, '填')
	}
	if got := extractVerificationCode("验证码", string(long)); got != "" {
		t.Fatalf("expected empty code for filler text, got %q", got)
	}
}

package main

import "fmt"

const zhJaFidelity = `原意优先：行为主体、作用对象、人物视角、动作方向、交际与逻辑关系、否定、可能性、难度及确定程度均不变，能否实现与实现难度不得互换；执行、请求、许可、委托不得互转，不擅自增删或弱化、强化；技术标识符、API 和字段名保持原样。输出前仅自检一次，先核对语义是否偏移，再核对场景句式与自然度，发现问题先修正；只输出译文，不输出检查过程。`

var prompts = map[string]string{
	TabColleague: `自然、礼貌适度的同事职场日语，使用です・ます体；保留交际意图、礼貌铺垫及其程度，不随意升级或降低敬语。` + zhJaFidelity,
	TabDocument:  `自然简洁的日语技术文档，使用常体，按表达功能选择句式：说明执行的处理时以动词基本形结束；条件句用「～場合」或「～とき」；规定或要求用「～こと」；赋值与设定用「～とする」或「～に設定する」；判断与定义用「～である」；省略处理用「～しない」或「～を省略する」。每句按主要功能选择一种句式，不机械混用；处理说明不得名词化或叠加其他句尾。` + zhJaFidelity,
	TabTestcase:  `自然的日文测试用例，使用常体；操作以「～する」表达，确认以「～ことを確認する」表达，期待值必须以「～こと」结尾，不能只用动词基本形或被动形结束，原文不含确认时不得添加「確認する」；保留类型区分，同句多项内容按原意自然连接。` + zhJaFidelity,
	TabJa2zh: `1、以准确、完整为优先，翻译为自然、流畅的简体中文，保留日文的语气、条件、范围、否定和因果关系。
2、不增删含义，不添加注释，只输出帮助用户理解原文的译文。
3、将【待翻译文本】翻译为中文。`,
}

func userPrompt(tab, text string) string {
	if tab == TabCustom {
		return text
	}
	if tab == TabColleague || tab == TabDocument || tab == TabTestcase {
		return fmt.Sprintf("请将以下文本翻译为日语。\n注意翻译的风格要严格符合【%s】\n\n%s", prompts[tab], text)
	}
	return fmt.Sprintf("【待翻译文本】\n%s\n\n【翻译任务】\n%s", text, prompts[tab])
}

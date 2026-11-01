.PHONY: bench report clean

# bench 一次只起一个被测对象，跑完全部轮次并生成报告
# 空闲进程不是零成本，混着跑会让结论失真，理由见 scripts/bench.sh 的注释
bench:
	./scripts/bench.sh

# report 只重新生成报告，不重跑压测
report:
	go build -o bin/report ./bootstrap/report && ./bin/report

clean:
	rm -rf bin results/*.json

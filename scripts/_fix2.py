import io

p = 'internal/ruleengine/template_test.go'
s = io.open(p, encoding='utf8', newline='').read()

LF = chr(10)
BS = chr(92)
broken = '"mixed-port: 7890' + LF + 'proxies: {{PROXIES}}' + LF + 'proxy-groups: []' + LF + '"'
fixed = '"mixed-port: 7890' + BS + 'nproxies: {{PROXIES}}' + BS + 'nproxy-groups: []' + BS + 'n"'

n = s.count(broken)
s = s.replace(broken, fixed)
io.open(p, 'w', encoding='utf8', newline='').write(s)
print('fixed', n, 'multi-line literal(s)')

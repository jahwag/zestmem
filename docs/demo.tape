Output zestmem-demo.mp4
Output zestmem-demo.gif

Set Shell "bash"
Set Width 1200
Set Height 675
Set FontSize 24
Set TypingSpeed 28ms
Set Theme { "name": "ZestMem", "black": "#17120d", "red": "#ff6b4a", "green": "#a8d86e", "yellow": "#ffc857", "blue": "#79b8ff", "magenta": "#d6a7ff", "cyan": "#65d6ce", "white": "#f7efe5", "brightBlack": "#66594d", "brightRed": "#ff8a70", "brightGreen": "#c3ed91", "brightYellow": "#ffda7b", "brightBlue": "#9dcdff", "brightMagenta": "#e5c5ff", "brightCyan": "#92ebe5", "brightWhite": "#fffaf3", "background": "#17120d", "foreground": "#f7efe5", "selectionBackground": "#493c31", "cursorAccent": "#17120d" }

Hide
Type "cd ~/zestmem"
Enter
Type "clear"
Enter
Show

Type "echo; echo '  ZestMem — durable memory for agent teams'; echo"
Enter
Sleep 2s

Type "docker compose ps --format json | jq -r '[.Service, .State, .Health] | @tsv'"
Enter
Sleep 4s

Type "curl -fsS http://127.0.0.1:8080/readyz && echo '✓ memory service ready'"
Enter
Sleep 3s

Type "echo; echo 'Restarting the memory service…'"
Enter
Type "docker compose restart memory >/dev/null"
Enter
Sleep 3s

Type "until curl -fsS http://127.0.0.1:8080/readyz >/dev/null; do sleep 1; done; echo '✓ PostgreSQL-backed memory is ready again'"
Enter
Sleep 4s

Type "echo; echo '  Agents reconnect. Their shared memory remains.'; echo '  github.com/jahwag/zestmem'; echo"
Enter
Sleep 5s

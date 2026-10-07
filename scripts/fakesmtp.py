"""Tiny fake SMTP server for tests: appends every received message to the file given as argv[1]. Listens on 127.0.0.1:2525."""
import socketserver, sys
class H(socketserver.StreamRequestHandler):
    def w(self, s): self.wfile.write((s+"\r\n").encode())
    def handle(self):
        self.w("220 fake ESMTP")
        data=False; buf=[]
        while True:
            line=self.rfile.readline()
            if not line: break
            l=line.decode().rstrip("\r\n")
            if data:
                if l==".":
                    open(sys.argv[1],"a").write("\n".join(buf)+"\n=====\n"); self.w("250 queued"); data=False; buf=[]
                else: buf.append(l)
                continue
            u=l.upper()
            if u.startswith("EHLO"): self.w("250-fake"); self.w("250 AUTH PLAIN")
            elif u.startswith("AUTH"): self.w("235 ok")
            elif u.startswith("MAIL") or u.startswith("RCPT"): self.w("250 ok")
            elif u=="DATA": self.w("354 go"); data=True
            elif u=="QUIT": self.w("221 bye"); break
            else: self.w("250 ok")
socketserver.TCPServer.allow_reuse_address=True
socketserver.ThreadingTCPServer(("127.0.0.1",2525),H).serve_forever()

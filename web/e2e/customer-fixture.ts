// Local fixtures only: synthetic credentials and mailbox contents. Never used by
// the shipped panel. SMTP uses a temporary CA so TLS validation stays enabled.
import {execFileSync} from "node:child_process";
import {readFile,writeFile} from "node:fs/promises";
import {createServer as createTLS} from "node:tls";
import {createServer as createHTTP} from "node:http";
import path from "node:path";
export const CUSTOMER_SMTP_PASSWORD="e2e-smtp-not-a-real-credential";
export const CUSTOMER_PAYMENT_PASSWORD1="e2e-checkout-not-a-real-credential";
export const CUSTOMER_PAYMENT_PASSWORD2="e2e-callback-not-a-real-credential";
export interface CustomerFixture{caFile:string;close:()=>Promise<void>}
export async function startCustomerFixture(directory:string):Promise<CustomerFixture>{
 const caFile=path.join(directory,"customer-ca.pem"),keyFile=path.join(directory,"customer-key.pem"),configFile=path.join(directory,"customer-openssl.cnf");
 await writeFile(configFile,"[req]\ndistinguished_name=dn\nx509_extensions=extensions\nprompt=no\n[dn]\nCN=localhost\n[extensions]\nsubjectAltName=IP:127.0.0.1,DNS:localhost\nbasicConstraints=critical,CA:TRUE\nkeyUsage=digitalSignature,keyEncipherment,keyCertSign\nextendedKeyUsage=serverAuth\n");
 execFileSync("openssl",["req","-x509","-newkey","rsa:2048","-nodes","-days","1","-config",configFile,"-keyout",keyFile,"-out",caFile],{stdio:"ignore"});
 const messages:Array<{to:string;link:string}>=[];
 const smtp=createTLS({key:await readFile(keyFile),cert:await readFile(caFile)},socket=>{
  let buffer="",data=false,body="",recipient="";
  socket.setEncoding("utf8");socket.setTimeout(15000,()=>socket.destroy());socket.on("error",()=>{});socket.write("220 localhost ESMTP\r\n");
  socket.on("data",chunk=>{
   buffer+=String(chunk);if(buffer.length+body.length>65536){socket.destroy();return;}
   for(let end=buffer.indexOf("\r\n");end>=0;end=buffer.indexOf("\r\n")){
    const line=buffer.slice(0,end);buffer=buffer.slice(end+2);
    if(data){if(line!=="."){body+=(line.startsWith("..")?line.slice(1):line)+"\r\n";continue;}
     const decoded=body.replace(/=\r\n/g,"").replace(/=([0-9A-F]{2})/gi,(_,hex:string)=>String.fromCharCode(parseInt(hex,16)));
     const link=decoded.match(/http:\/\/localhost:48180\/portal#[^\s]+/)?.[0]??"";
     messages.push({to:recipient,link});if(messages.length>100)messages.shift();data=false;body="";socket.write("250 queued\r\n");continue;
    }
    if(line.startsWith("EHLO"))socket.write("250-localhost\r\n250 AUTH PLAIN\r\n");
    else if(line.startsWith("AUTH PLAIN ")){const auth=Buffer.from(line.slice(11),"base64").toString();socket.write(auth===`\0sender\0${CUSTOMER_SMTP_PASSWORD}`?"235 authenticated\r\n":"535 rejected\r\n");}
    else if(line.startsWith("MAIL FROM:"))socket.write("250 accepted\r\n");
    else if(line.startsWith("RCPT TO:")){recipient=line.match(/<([^>]+)>/)?.[1]??"";socket.write("250 accepted\r\n");}
    else if(line==="DATA"){data=true;socket.write("354 send data\r\n");}
    else if(line==="QUIT"){socket.end("221 bye\r\n");}
    else socket.write("500 unsupported\r\n");
   }
  });
 });
 const capture=createHTTP((req,res)=>{const url=new URL(req.url??"/","http://localhost");res.setHeader("Content-Type","application/json");res.end(JSON.stringify(messages.filter(m=>m.to===url.searchParams.get("to"))));});
 const listen=(server:typeof capture|typeof smtp,port:number)=>new Promise<void>((resolve,reject)=>{server.once("error",reject);server.listen(port,"127.0.0.1",()=>{server.off("error",reject);resolve();});});
 await listen(smtp,48197);
 try{await listen(capture,48198);}catch(e){smtp.close();throw e;}
 return{caFile,close:async()=>{capture.closeAllConnections();await Promise.all([new Promise<void>(resolve=>capture.close(()=>resolve())),new Promise<void>(resolve=>smtp.close(()=>resolve()))]);}};
}

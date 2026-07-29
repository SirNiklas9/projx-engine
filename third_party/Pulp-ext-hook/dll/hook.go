// projxhook: a c-shared DLL that IAT-hooks the named-pipe/file APIs of the process
// it is loaded into and splices the AppContainer-local "LOCAL\" segment into
// libuv's hardcoded global pipe name (\\?\pipe\uv\... -> \\?\pipe\LOCAL\uv\...) so
// pipe creation succeeds under an AppContainer token. It ALSO hooks CreateProcess
// so every child the host spawns is created suspended, gets this same DLL injected
// + armed, then resumed — propagating the cage hook across the whole process tree
// (otherwise a libuv-based grandchild creating its own pipes would spin/hang).
// Defensive sandboxing.
//
// Build:  go build -buildmode=c-shared -ldflags '-linkmode external -extldflags "-static"' -o projxhook.dll .
package main

/*
#include <windows.h>
#include <stdlib.h>
#include <string.h>
#include <wchar.h>

typedef HANDLE (WINAPI *CNPW_t)(LPCWSTR,DWORD,DWORD,DWORD,DWORD,DWORD,DWORD,LPSECURITY_ATTRIBUTES);
typedef HANDLE (WINAPI *CNPA_t)(LPCSTR ,DWORD,DWORD,DWORD,DWORD,DWORD,DWORD,LPSECURITY_ATTRIBUTES);
typedef HANDLE (WINAPI *CFW_t )(LPCWSTR,DWORD,DWORD,LPSECURITY_ATTRIBUTES,DWORD,DWORD,HANDLE);
typedef HANDLE (WINAPI *CFA_t )(LPCSTR ,DWORD,DWORD,LPSECURITY_ATTRIBUTES,DWORD,DWORD,HANDLE);
typedef BOOL   (WINAPI *CPW_t )(LPCWSTR,LPWSTR,LPSECURITY_ATTRIBUTES,LPSECURITY_ATTRIBUTES,BOOL,DWORD,LPVOID,LPCWSTR,LPSTARTUPINFOW,LPPROCESS_INFORMATION);
typedef BOOL   (WINAPI *CPA_t )(LPCSTR ,LPSTR ,LPSECURITY_ATTRIBUTES,LPSECURITY_ATTRIBUTES,BOOL,DWORD,LPVOID,LPCSTR ,LPSTARTUPINFOA,LPPROCESS_INFORMATION);

typedef BOOL  (WINAPI *EnumProcMods_t)(HANDLE,HMODULE*,DWORD,LPDWORD);
typedef DWORD (WINAPI *GetModBaseW_t )(HANDLE,HMODULE,LPWSTR,DWORD);

static CNPW_t orig_CNPW=NULL; static CNPA_t orig_CNPA=NULL;
static CFW_t  orig_CFW =NULL; static CFA_t  orig_CFA =NULL;
static CPW_t  orig_CPW =NULL; static CPA_t  orig_CPA =NULL;

static wchar_t g_dllpath[260];
static DWORD   g_armRVA = 0;   // RVA of the exported Arm within this DLL

// Insert LOCAL\ after a leading \\?\pipe\ or \\.\pipe\ (9 chars). Leaks (rare).
static LPCWSTR rw_w(LPCWSTR n){
    if(!n) return n; const size_t p=9;
    if(wcsncmp(n,L"\\\\?\\pipe\\",p)!=0 && wcsncmp(n,L"\\\\.\\pipe\\",p)!=0) return n;
    if(wcsncmp(n+p,L"LOCAL\\",6)==0) return n;
    const wchar_t* rest=n+p; size_t rl=wcslen(rest);
    wchar_t* b=(wchar_t*)malloc((p+6+rl+1)*sizeof(wchar_t)); if(!b) return n;
    wcsncpy(b,n,p); wcsncpy(b+p,L"LOCAL\\",6); wcscpy(b+p+6,rest); return b;
}
static LPCSTR rw_a(LPCSTR n){
    if(!n) return n; const size_t p=9;
    if(strncmp(n,"\\\\?\\pipe\\",p)!=0 && strncmp(n,"\\\\.\\pipe\\",p)!=0) return n;
    if(strncmp(n+p,"LOCAL\\",6)==0) return n;
    const char* rest=n+p; size_t rl=strlen(rest);
    char* b=(char*)malloc(p+6+rl+1); if(!b) return n;
    strncpy(b,n,p); strncpy(b+p,"LOCAL\\",6); strcpy(b+p+6,rest); return b;
}

static HANDLE WINAPI h_CNPW(LPCWSTR n,DWORD a,DWORD b,DWORD c,DWORD d,DWORD e,DWORD f,LPSECURITY_ATTRIBUTES s){ return orig_CNPW(rw_w(n),a,b,c,d,e,f,s); }
static HANDLE WINAPI h_CNPA(LPCSTR  n,DWORD a,DWORD b,DWORD c,DWORD d,DWORD e,DWORD f,LPSECURITY_ATTRIBUTES s){ return orig_CNPA(rw_a(n),a,b,c,d,e,f,s); }
static HANDLE WINAPI h_CFW (LPCWSTR n,DWORD a,DWORD b,LPSECURITY_ATTRIBUTES s,DWORD c,DWORD d,HANDLE h){ return orig_CFW(rw_w(n),a,b,s,c,d,h); }
static HANDLE WINAPI h_CFA (LPCSTR  n,DWORD a,DWORD b,LPSECURITY_ATTRIBUTES s,DWORD c,DWORD d,HANDLE h){ return orig_CFA(rw_a(n),a,b,s,c,d,h); }

// Inject THIS dll into a (suspended) child and arm it, so the hook propagates down
// the whole process tree. Best-effort: on any failure the child still runs (just
// unhooked — same as before this feature), never blocked.
static void inject_into_child(HANDLE hProc){
    if(g_armRVA==0 || !hProc) return;
    SIZE_T n=(wcslen(g_dllpath)+1)*sizeof(wchar_t);
    void* rp=VirtualAllocEx(hProc,NULL,n,MEM_COMMIT|MEM_RESERVE,PAGE_READWRITE);
    if(!rp) return;
    if(!WriteProcessMemory(hProc,rp,g_dllpath,n,NULL)) return;
    HMODULE k32=GetModuleHandleW(L"kernel32.dll");
    FARPROC pLoad=GetProcAddress(k32,"LoadLibraryW");
    HANDLE th=CreateRemoteThread(hProc,NULL,0,(LPTHREAD_START_ROUTINE)pLoad,rp,0,NULL);
    if(!th) return;
    WaitForSingleObject(th,INFINITE); CloseHandle(th);
    EnumProcMods_t em=(EnumProcMods_t)GetProcAddress(k32,"K32EnumProcessModules");
    GetModBaseW_t  gb=(GetModBaseW_t )GetProcAddress(k32,"K32GetModuleBaseNameW");
    if(!em||!gb) return;
    const wchar_t* bn=g_dllpath; for(const wchar_t* q=g_dllpath;*q;q++) if(*q=='\\'||*q=='/') bn=q+1;
    HMODULE mods[1024]; DWORD need=0;
    if(!em(hProc,mods,sizeof(mods),&need)) return;
    DWORD cnt=need/sizeof(HMODULE); if(cnt>1024) cnt=1024;
    for(DWORD i=0;i<cnt;i++){
        wchar_t nm[260]; if(!gb(hProc,mods[i],nm,260)) continue;
        if(_wcsicmp(nm,bn)==0){
            void* armAddr=(BYTE*)mods[i]+g_armRVA;
            HANDLE at=CreateRemoteThread(hProc,NULL,0,(LPTHREAD_START_ROUTINE)armAddr,NULL,0,NULL);
            if(at){ WaitForSingleObject(at,INFINITE); CloseHandle(at); }
            return;
        }
    }
}

static BOOL WINAPI h_CPW(LPCWSTR app,LPWSTR cmd,LPSECURITY_ATTRIBUTES pa,LPSECURITY_ATTRIBUTES ta,BOOL inh,DWORD fl,LPVOID env,LPCWSTR dir,LPSTARTUPINFOW si,LPPROCESS_INFORMATION pi){
    BOOL r=orig_CPW(app,cmd,pa,ta,inh,fl|CREATE_SUSPENDED,env,dir,si,pi);
    if(r){ inject_into_child(pi->hProcess); if(!(fl&CREATE_SUSPENDED)) ResumeThread(pi->hThread); }
    return r;
}
static BOOL WINAPI h_CPA(LPCSTR app,LPSTR cmd,LPSECURITY_ATTRIBUTES pa,LPSECURITY_ATTRIBUTES ta,BOOL inh,DWORD fl,LPVOID env,LPCSTR dir,LPSTARTUPINFOA si,LPPROCESS_INFORMATION pi){
    BOOL r=orig_CPA(app,cmd,pa,ta,inh,fl|CREATE_SUSPENDED,env,dir,si,pi);
    if(r){ inject_into_child(pi->hProcess); if(!(fl&CREATE_SUSPENDED)) ResumeThread(pi->hThread); }
    return r;
}

static void patch_slot(void** slot,void* hook,void** save){
    *save=*slot; DWORD old;
    VirtualProtect(slot,sizeof(void*),PAGE_READWRITE,&old);
    *slot=hook;
    VirtualProtect(slot,sizeof(void*),old,&old);
}

static int arm(){
    BYTE* base=(BYTE*)GetModuleHandleW(NULL);
    if(!base) return 0;

    // Resolve our own module path + Arm RVA (for propagating to children).
    if(g_armRVA==0){
        HMODULE self=NULL;
        GetModuleHandleExW(GET_MODULE_HANDLE_EX_FLAG_FROM_ADDRESS|GET_MODULE_HANDLE_EX_FLAG_UNCHANGED_REFCOUNT,(LPCWSTR)&arm,&self);
        if(self){
            GetModuleFileNameW(self,g_dllpath,260);
            FARPROC a=GetProcAddress(self,"Arm");
            if(a) g_armRVA=(DWORD)((BYTE*)a-(BYTE*)self);
        }
    }

    IMAGE_DOS_HEADER* dos=(IMAGE_DOS_HEADER*)base;
    IMAGE_NT_HEADERS* nt=(IMAGE_NT_HEADERS*)(base+dos->e_lfanew);
    IMAGE_DATA_DIRECTORY d=nt->OptionalHeader.DataDirectory[IMAGE_DIRECTORY_ENTRY_IMPORT];
    if(d.VirtualAddress==0) return 0;
    int patched=0;
    IMAGE_IMPORT_DESCRIPTOR* desc=(IMAGE_IMPORT_DESCRIPTOR*)(base+d.VirtualAddress);
    for(; desc->Name; desc++){
        DWORD intRVA=desc->OriginalFirstThunk?desc->OriginalFirstThunk:desc->FirstThunk;
        IMAGE_THUNK_DATA* oft=(IMAGE_THUNK_DATA*)(base+intRVA);
        IMAGE_THUNK_DATA* ft =(IMAGE_THUNK_DATA*)(base+desc->FirstThunk);
        for(; oft->u1.AddressOfData; oft++, ft++){
            if(oft->u1.Ordinal & IMAGE_ORDINAL_FLAG64) continue;
            IMAGE_IMPORT_BY_NAME* ibn=(IMAGE_IMPORT_BY_NAME*)(base+oft->u1.AddressOfData);
            const char* fn=(const char*)ibn->Name;
            void** slot=(void**)&ft->u1.Function;
            if(strcmp(fn,"CreateNamedPipeW")==0){ patch_slot(slot,(void*)h_CNPW,(void**)&orig_CNPW); patched++; }
            else if(strcmp(fn,"CreateNamedPipeA")==0){ patch_slot(slot,(void*)h_CNPA,(void**)&orig_CNPA); patched++; }
            else if(strcmp(fn,"CreateFileW")==0){ patch_slot(slot,(void*)h_CFW,(void**)&orig_CFW); patched++; }
            else if(strcmp(fn,"CreateFileA")==0){ patch_slot(slot,(void*)h_CFA,(void**)&orig_CFA); patched++; }
            else if(strcmp(fn,"CreateProcessW")==0){ patch_slot(slot,(void*)h_CPW,(void**)&orig_CPW); patched++; }
            else if(strcmp(fn,"CreateProcessA")==0){ patch_slot(slot,(void*)h_CPA,(void**)&orig_CPA); patched++; }
        }
    }
    return patched;
}
*/
import "C"

func main() {}

//export Arm
func Arm() uintptr {
	return uintptr(C.arm())
}

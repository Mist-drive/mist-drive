export namespace apiclient {
	
	export class ObjectInfo {
	    key: string;
	    size: number;
	    etag: string;
	    lastModified: string;
	
	    static createFrom(source: any = {}) {
	        return new ObjectInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.size = source["size"];
	        this.etag = source["etag"];
	        this.lastModified = source["lastModified"];
	    }
	}
	export class ListResponse {
	    objects: ObjectInfo[];
	    processing: string[];
	
	    static createFrom(source: any = {}) {
	        return new ListResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.objects = this.convertValues(source["objects"], ObjectInfo);
	        this.processing = source["processing"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class PublicUser {
	    id: string;
	    login: string;
	    role: string;
	    quotaBytes: number;
	    usedBytes: number;
	
	    static createFrom(source: any = {}) {
	        return new PublicUser(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.login = source["login"];
	        this.role = source["role"];
	        this.quotaBytes = source["quotaBytes"];
	        this.usedBytes = source["usedBytes"];
	    }
	}

}

export namespace settings {
	
	export class SyncFolder {
	    local: string;
	    remotePrefix: string;
	    upload: boolean;
	    download: boolean;
	    enabled: boolean;
	
	    static createFrom(source: any = {}) {
	        return new SyncFolder(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.local = source["local"];
	        this.remotePrefix = source["remotePrefix"];
	        this.upload = source["upload"];
	        this.download = source["download"];
	        this.enabled = source["enabled"];
	    }
	}
	export class Settings {
	    apiUrl: string;
	    jwt: string;
	    login: string;
	    rememberLogin: boolean;
	    folders: SyncFolder[];
	    maxConcurrentUploads: number;
	    maxUploadRateKBps: number;
	    startOnLaunch: boolean;
	    closeToTray: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Settings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.apiUrl = source["apiUrl"];
	        this.jwt = source["jwt"];
	        this.login = source["login"];
	        this.rememberLogin = source["rememberLogin"];
	        this.folders = this.convertValues(source["folders"], SyncFolder);
	        this.maxConcurrentUploads = source["maxConcurrentUploads"];
	        this.maxUploadRateKBps = source["maxUploadRateKBps"];
	        this.startOnLaunch = source["startOnLaunch"];
	        this.closeToTray = source["closeToTray"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace sync {
	
	export class LogEntry {
	    // Go type: time
	    time: any;
	    action: string;
	    file: string;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new LogEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.time = this.convertValues(source["time"], null);
	        this.action = source["action"];
	        this.file = source["file"];
	        this.error = source["error"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Status {
	    running: boolean;
	    // Go type: time
	    lastPass: any;
	    lastError: string;
	    uploaded: number;
	    downloaded: number;
	    skipped: number;
	    errors: number;
	    totalUploaded: number;
	    totalDownloaded: number;
	    inFlight: string;
	
	    static createFrom(source: any = {}) {
	        return new Status(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.running = source["running"];
	        this.lastPass = this.convertValues(source["lastPass"], null);
	        this.lastError = source["lastError"];
	        this.uploaded = source["uploaded"];
	        this.downloaded = source["downloaded"];
	        this.skipped = source["skipped"];
	        this.errors = source["errors"];
	        this.totalUploaded = source["totalUploaded"];
	        this.totalDownloaded = source["totalDownloaded"];
	        this.inFlight = source["inFlight"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}


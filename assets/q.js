/*Scripts For Request - SomeBottle*/
var $ = new Object();
$.ls = new Array();
$.lss = '';
$.hash = window.location.href;
var SC = function(e) {
		if (e == 'body') {
			return document.body;
		} else {
			return document.getElementById(e);
		}
	}
$.chash = function(h) { /*校验hash*/
	if ($.tr(window.location.href) == h) {
		return true;
	} else {
		return false;
	}
}
$.script = function(url) {
	var script = document.createElement("script");
	var exist = false;
	for (var up in $.ls) {
		if ($.ls[up] == url) {
			exist = true;
			break;
		}
	}
	if (!exist) {
		$.ls[$.ls.length] = url;
		script.type = "text/javascript";
		script.src = url;
		document.body.appendChild(script);
	}
}
$.tr = function(url) {
	var a = url;
	a = a.split('?');
	if ((a.length - 1) > 1 || url.indexOf('!') !== -1) {
		if (url.indexOf('?') !== -1) {
			a.pop();
		}
	}
	var u = '';
	for (var i in a) {
		u = u + a[i] + '?';
	}
	u = u.substring(0, u.length - 1);
	return u;
}
$.op = function(v, e) {
	ht = SC(e);
	if (Number(v) == 1) {
		ht.style.transition = '0.5s ease';
		ht.style.opacity = '1';
	} else {
		ht.style.transition = 'none';
		ht.style.opacity = '0';
	}
}
$.rm = function(e) {
	SC(e).parentNode.removeChild(SC(e));
}
$.ht = function(h, e) {
	ht = SC(e);
	ht.innerHTML = h;
	os = ht.getElementsByTagName('script');
	var scr = '';
	for (var o = 0; o < os.length; o++) {
		scr = scr + os[o].innerHTML;
		if (os[o].src !== undefined && os[o].src !== null && os[o].src !== '') {
			$.script(os[o].src);
		} else {
			if ($.lss !== scr || $.hash !== window.location.href) {
				setTimeout(os[o].innerHTML,0);
			}
		}
	}
}
$.aj = function(p, d, sf, m) { /*(path,data,success or fail,method)*/
	var xhr = new XMLHttpRequest();
	var hm = '';
	for (var ap in d) {
		hm = hm + ap + '=' + d[ap] + '&';
	}
	hm = hm.substring(0, hm.length - 1);
	if (m !== 'multipart/form-data') {
		xhr.open(m, p, true);
		xhr.setRequestHeader("Content-type", "application/x-www-form-urlencoded");
		xhr.send(hm);
	} else {
		xhr.open('post', p, true);
		xhr.send(d);
	}
	xhr.onreadystatechange = function() {
		if (xhr.readyState == 4 && xhr.status == 200) {
			sf.success(xhr.responseText);
		} else if (xhr.readyState == 4 && xhr.status !== 200) {
			sf.failed(xhr.status);
		}
	};
}
<?php
/*后台处理中枢*/
error_reporting(E_ALL ^ E_NOTICE ^ E_WARNING);
header('Content-type:text/json;charset=utf-8');
date_default_timezone_set("Asia/Shanghai");
$act = $_GET['a']; /*Action*/
$rt = array('code' => 1);
/*Initialization*/
function p($p) {
    return './data/' . $p;
}
if (!is_dir('./data')) {
    mkdir('./data');
}
function fileconst($arr) { /*储存文件组装器*/
    $re = '<?php ';
    foreach ($arr as $k => $v) {
        $re.= '$' . (is_array($v) ? ($k . '=' . var_export($v, true) . ';') : ($k . (is_numeric($v) ? ('=' . $v . ';') : ('=\'' . $v . '\';'))));
    }
    return $re . '?>';
}
if (!file_exists(p('frameindex.php'))) {
    file_put_contents(p('frameindex.php'), fileconst(array('frnum' => 0, 'frames' => array())));
}
if (!is_dir(p('users'))) {
    mkdir(p('users'));
}
if (!is_dir(p('frames'))) {
    mkdir(p('frames'));
}
/*Finished*/
function config($k) {
    require 'config.php';
    return $config[$k];
}
function arrdeleol($arrs) { /*数组base64处理器(数组,是否decode)*/
    foreach ($arrs as $k => $v) {
        $arrs[$k] = str_ireplace(PHP_EOL, '', filter($v));
    }
    return $arrs;
}
function framebase64decode($arr) {
    $arr['domains'] = $arr['domains'];
    $arr['name'] = base64_decode($arr['name']);
    $arr['intro'] = base64_decode($arr['intro']);
    return $arr;
}
function removedir($path) {
    $arr = scandir($path);
    foreach ($arr as $f) {
        if ($f !== '.' && $f !== '..') {
            $file = $path . '/' . $f;
            if (is_dir($file)) {
                removedir($file);
                @rmdir($file);
            } else {
                unlink($file);
            }
        }
    }
    @rmdir($path);
}
function commentdeleter($rid, $floors, $frameid, $akey) {
    require p('frames/' . $frameid . '/' . $akey . '/index.php'); /*获取楼层索引*/
    require p('frames/' . $frameid . '/topindex.php'); /*获取总置顶索引*/
    $rid = intval($rid);
    $idr = commentindexer($rid, $floors);
    $rt = array('success' => false);
    if ($idr['cid'] !== 'failed') { /*找到了*/
        require p('frames/' . $frameid . '/' . $akey . '/c' . $rid . '.php');
        $cid = $idr['cid'];
        $subcid = $idr['subcid'];
        if ($idr['sub']) {
            unset($floors[$cid]['r'][$subcid]);
            $commentsnum-= 1;
        } else {
            $subs = $floors[$cid]['r'];
            foreach ($subs as $v) {
                @unlink(p('frames/' . $frameid . '/' . $akey . '/c' . $v['m'] . '.php')); /*删除楼层下子评论储存文件*/
            }
            unset($floors[$cid]); /*删除评论索引*/
            $commentsnum-= (count($subs) + 1); /*把楼层的子评论全算进去*/
            /*如果有置顶也一起删了*/
            if ($tops['c'] === $rid) {
                $tops = array();
                file_put_contents(p('frames/' . $frameid . '/topindex.php'), fileconst(array('tops' => $tops)));
            } else if ($thetop['c'] === $rid) {
                $thetop = array();
            }
        }
        @unlink(p('frames/' . $frameid . '/' . $akey . '/c' . $rid . '.php')); /*删除评论储存文件*/
        $rt['success'] = true;
        file_put_contents(p('frames/' . $frameid . '/' . $akey . '/index.php'), fileconst(array('comments' => $comments, 'thetop' => $thetop, 'commentsnum' => $commentsnum, 'orgakey' => $orgakey, 'floors' => $floors)));
    } else {
        $rt['msg'] = '无此评论';
    }
    return $rt;
}
function findcmts($cm, $query) {
    $f = false;
    foreach ($query as $k => $v) {
        if ($k == 'u' && (@stripos($cm['name'], $v) !== false || @stripos($cm['owner'], $v) !== false)) {
            $f = true;
        } else if ($k == 'c' && @stripos($cm['content'], $v) !== false) {
            $f = true;
        } else {
            return false;
        }
    }
    return $f;
}
function commentindexer($rid, $arr) {
    $sub = false; /*是否是回复中的子评论*/
    $subfind = 'no';
    $find = 'no';
    $parent = 'no';
    foreach ($arr as $k => $v) {
        if ($v['m'] === intval($rid)) { /*严格判断*/
            $parent = $v['m'];
            $find = $k;
            break;
        }
        foreach ($v['r'] as $subk => $subv) {
            if ($subv['m'] === intval($rid)) { /*严格判断*/
                $parent = $v['m'];
                $find = $k;
                $sub = true;
                $subfind = $subk;
                break;
            }
        }
        if ($find !== 'no') break;
    }
    $return = array('cid' => $find !== 'no' ? $find : 'failed', 'sub' => $sub, 'subcid' => $subfind, 'parentrid' => $parent);
    return $return;
}
function filter($c) { /*xss过滤*/
    return addslashes(htmlspecialchars(strip_tags(trim($c))));
}
@session_start();
$usr = $_SESSION['commentuser']; /*get user details*/
$uid = $usr['id'];
if (isset($usr)) {
    if (!file_exists(p('users/' . $uid . '.php'))) {
        file_put_contents(p('users/' . $uid . '.php'), fileconst(array('myindex' => array(), 'myframes' => array(), 'lastcount' => 0, 'commentsin' => 0)));
    }
    if ($act == 'create') {
        if (config('opentoeveryone') || (!config('opentoeveryone') && in_array($uid, config('allowedones'), true))) {
            $name = $_POST['st'];
            $intro = $_POST['it'];
            if (!empty($name) && !empty($intro) && strlen($name) <= 20 && strlen($intro) <= 100) {
                $name = base64_encode(filter($name));
                $intro = base64_encode(filter($intro));
                require p('frameindex.php'); /*import frameindex*/
                $frameid = substr(md5($uid . time() . $frnum), 8, 16); /*获得评论框独有ID*/
                $frames[$frameid] = array('owner' => $uid, 'domains' => array()); /*储存索引*/
                mkdir(p('frames/' . $frameid)); /*创建框架储存区*/
                require p('users/' . $uid . '.php'); /*import userindex*/
                array_push($myindex, $frameid); /*储存user的框架专属ID*/
                $framenum = count($myframes); /*计算user有多少框架*/
                $myframes[$frameid] = array('name' => $name, 'intro' => $intro, 'domains' => array()); /*在user的档案里储存框架详细信息*/
                if ($framenum < config('maxcreatenum')) {
                    $frnum+= 1;
                    file_put_contents(p('frameindex.php'), fileconst(array('frnum' => $frnum, 'frames' => $frames)));
                    file_put_contents(p('users/' . $uid . '.php'), fileconst(array('myindex' => $myindex, 'myframes' => $myframes, 'lastcount' => $lastcount, 'commentsin' => $commentsin)));
                    $rt['msg'] = 'success';
                } else {
                    $rt['code'] = 0;
                    $rt['msg'] = '超过创建上限';
                }
            } else {
                $rt['code'] = 0;
                $rt['msg'] = '不能为空或字数过多';
            }
        } else {
            $rt['code'] = 0;
            $rt['msg'] = '你没有权限创建评论框';
        }
    } else if ($act == 'getowned') {
        require p('users/' . $uid . '.php'); /*import userindex*/
        $frids = $myframes;
        if (!isset($frids) || empty($frids)) {
            $rt['code'] = 0;
            $rt['msg'] = 'empty';
        } else {
            $rt['msg'] = 'success';
            $rt['data'] = array();
            foreach ($frids as $k => $v) {
                $arr = $v;
                $arr = framebase64decode($arr);
                $id = array_search($k, $myindex);
                $arr['id'] = $id;
                array_push($rt['data'], $arr);
            }
        }
    } else if ($act == 'getdetail') {
        require p('users/' . $uid . '.php'); /*import userindex*/
        $frid = $_POST['id'];
        $myfrids = $myindex;
        if ((!empty($frid) || intval($frid) == 0) && array_key_exists(intval($frid), $myfrids)) {
            $id = $myfrids[$frid];
            $arr = $myframes[$id];
            $arr = framebase64decode($arr);
            $rt['data'] = $arr;
        } else {
            $rt['code'] = 0;
            $rt['msg'] = '你的档案中没有此评论框';
        }
    } else if ($act == 'edit') {
        $name = $_POST['st'];
        $intro = $_POST['it'];
        $frid = $_POST['id'];
        $ds = array_filter(explode(',', $_POST['ds']));
        $domains = arrdeleol($ds);
        $dmlen = count($ds);
        require p('users/' . $uid . '.php'); /*import userindex*/
        $myfrids = $myindex;
        if ((!empty($frid) || intval($frid) == 0) && array_key_exists(intval($frid), $myfrids)) {
            if (!empty($name) && !empty($intro) && !empty($domains) && strlen($name) <= 20 && strlen($intro) <= 100 && $dmlen <= 50) {
                $id = $myfrids[$frid];
                $name = base64_encode(filter($name));
                $intro = base64_encode(filter($intro));
                $myframes[$id] = array('name' => $name, 'intro' => $intro, 'domains' => $domains);
                file_put_contents(p('users/' . $uid . '.php'), fileconst(array('myindex' => $myindex, 'myframes' => $myframes, 'lastcount' => $lastcount, 'commentsin' => $commentsin)));
                require p('frameindex.php'); /*import frameindex*/
                $frames[$id]['domains'] = $domains;
                file_put_contents(p('frameindex.php'), fileconst(array('frnum' => $frnum, 'frames' => $frames)));
            } else {
                $rt['code'] = 0;
                $rt['msg'] = '不能为空或字数过多';
            }
        } else {
            $rt['code'] = 0;
            $rt['msg'] = '你的档案中没有此评论框';
        }
    } else if ($act == 'del') {
        require p('users/' . $uid . '.php'); /*import userindex*/
        $frid = $_POST['id'];
        $myfrids = $myindex;
        if ((!empty($frid) || intval($frid) == 0) && array_key_exists(intval($frid), $myfrids)) {
            $id = $myfrids[$frid];
            removedir(p('frames/' . $id)); /*删除框架储存区*/
            unset($myframes[$id]);
            unset($myindex[$frid]);
            file_put_contents(p('users/' . $uid . '.php'), fileconst(array('myindex' => $myindex, 'myframes' => $myframes, 'lastcount' => $lastcount, 'commentsin' => $commentsin)));
            require p('frameindex.php'); /*import frameindex*/
            unset($frames[$id]);
            file_put_contents(p('frameindex.php'), fileconst(array('frnum' => $frnum, 'frames' => $frames)));
        } else {
            $rt['code'] = 0;
            $rt['msg'] = '你的档案中没有此评论框';
        }
    } else if ($act == 'search') {
        require p('users/' . $uid . '.php'); /*import userindex*/
        $frid = $_POST['id'];
        $words = trim(filter($_POST['v']));
        $myfrids = $myindex;
        if ((!empty($frid) || intval($frid) == 0) && array_key_exists(intval($frid), $myfrids)) {
            $frameid = $myfrids[$frid];
            require p('frames/' . $frameid . '/topindex.php');
            if (file_exists(p('frames/' . $frameid . '/akeyindex.php'))) {
                require p('frames/' . $frameid . '/akeyindex.php');
                $data = array();
                foreach ($akeys as $k => $v) {
                    $rakey = base64_decode($v);
                    if (stripos($rakey, $words) !== false) {
                        array_push($data, array('k' => filter($rakey), 'i' => $k));
                    }
                }
                $rt['alltop'] = isset($tops['c']) ? true : false; /*标注是否有总置顶*/
                $rt['data'] = $data;
            } else {
                $rt['code'] = 0;
                $rt['msg'] = '暂时没有任何akey';
            }
        } else {
            $rt['code'] = 0;
            $rt['msg'] = '你的档案中没有此评论框';
        }
    } else if ($act == 'scmt') {
        require p('users/' . $uid . '.php'); /*import userindex*/
        $frid = $_POST['id'];
        $akey = $_POST['a'];
        $words = $_POST['w'];
        $myfrids = $myindex;
        /*Consult analyzer*/
        $params = array_filter(explode(',', $words));
        $query = array();
        foreach ($params as $v) {
            $ps = array_filter(explode(':', $v));
            if (count($ps) >= 2) $query[strtolower($ps[0]) ] = $ps[1]; /*construct query*/
        }
        if (!isset($query['u']) && !isset($query['c'])) {
            $rt['code'] = 0;
            $rt['msg'] = '查询参数缺失';
        } else if ((!empty($frid) || intval($frid) == 0) && array_key_exists(intval($frid), $myfrids)) {
            $frameid = $myfrids[$frid];
            if (file_exists(p('frames/' . $frameid . '/' . $akey . '/index.php'))) {
                require p('frames/' . $frameid . '/' . $akey . '/index.php');
                $data = array();
                foreach ($floors as $k => $v) {
                    $search = array();
                    $find = false;
                    require p('frames/' . $frameid . '/' . $akey . '/c' . $v['m'] . '.php');
                    $cm['content'] = base64_decode($cm['content']);
                    $cm['date'] = date('Y年n月j日', $cm['unix']);
                    $search['m'] = $cm;
                    $find = findcmts($cm, $query);
                    if (is_array($v['r'])) {
                        foreach ($v['r'] as $key => $val) {
                            require p('frames/' . $frameid . '/' . $akey . '/c' . $val['m'] . '.php');
                            $cm['content'] = base64_decode($cm['content']);
                            $cm['date'] = date('Y年n月j日', $cm['unix']);
                            $find = $find ? $find : findcmts($cm, $query);
                            $search['r'][$key] = array();
                            $search['r'][$key]['m'] = $cm;
                        }
                    }
                    if ($find) $data[$k] = $search;
                }
                $rt['top'] = isset($thetop['c']) ? true : false; /*标注是否有akey对应置顶*/
                $rt['data'] = $data;
            } else {
                $rt['code'] = 0;
                $rt['msg'] = '你的框框中没有此akey';
            }
        } else {
            $rt['code'] = 0;
            $rt['msg'] = '你的档案中没有此评论框';
        }
    } else if ($act == 'delcm') {
        require p('users/' . $uid . '.php'); /*import userindex*/
        $frid = $_POST['id'];
        $akey = $_POST['a'];
        $rid = $_POST['rid'];
        $mode = $_POST['md'];
        $myfrids = $myindex;
        if ((!empty($frid) || intval($frid) == 0) && array_key_exists(intval($frid), $myfrids)) {
            $frameid = $myfrids[$frid];
            if (file_exists(p('frames/' . $frameid . '/' . $akey . '/index.php'))) {
                if ($mode == 'single') {
                    $re = commentdeleter($rid, $floors, $frameid, $akey);
                    $rt['code'] = $re['success'] ? 1 : 0;
                    $rt['msg'] = $re['success'] ? '' : $re['msg'];
                } else if ($mode == 'multi') {
                    $rids = explode(',', $rid);
                    $finished = 0;
                    foreach ($rids as $v) {
                        $re = commentdeleter($v, $floors, $frameid, $akey);
                        $finished+= $re['success'] ? 1 : 0;
                    }
                    $rt['code'] = $finished > 0 ? 1 : 0;
                    $rt['msg'] = $finished > 0 ? '' : '没有评论被删除';
                } else {
                    $rt['code'] = 0;
                    $rt['msg'] = '模式未指定';
                }
            } else {
                $rt['code'] = 0;
                $rt['msg'] = '你的框框中没有此akey';
            }
        } else {
            $rt['code'] = 0;
            $rt['msg'] = '你的档案中没有此评论框';
        }
    } else if ($act == 'top') {
        require p('users/' . $uid . '.php'); /*import userindex*/
        $frid = $_POST['id'];
        $akey = $_POST['a'];
        $rid = $_POST['rid'];
        $mode = $_POST['md'];
        $myfrids = $myindex;
        if ((!empty($frid) || intval($frid) == 0) && array_key_exists(intval($frid), $myfrids)) {
            $frameid = $myfrids[$frid];
            if (file_exists(p('frames/' . $frameid . '/' . $akey . '/index.php')) && file_exists(p('frames/' . $frameid . '/topindex.php'))) {
                require p('frames/' . $frameid . '/' . $akey . '/index.php');
                $idr = commentindexer($rid, $floors);
                if ($idr['cid'] !== 'failed' && !$idr['sub']) { /*找到了，置顶的不能是回复*/
                    require p('frames/' . $frameid . '/topindex.php');
                    $cid = $idr['cid'];
                    $rid = intval($rid);
                    if ($mode == 'single' && count($thetop) == 0 && !($tops['c'] === $rid)) {
                        $thetop = array('c' => $rid);
                        file_put_contents(p('frames/' . $frameid . '/' . $akey . '/index.php'), fileconst(array('comments' => $comments, 'thetop' => $thetop, 'commentsnum' => $commentsnum, 'orgakey' => $orgakey, 'floors' => $floors)));
                    } else if ($mode == 'all' && count($tops) == 0 && !($thetop['c'] === $rid)) {
                        $tops = array('c' => $rid, 'akey' => $akey);
                        file_put_contents(p('frames/' . $frameid . '/topindex.php'), fileconst(array('tops' => $tops)));
                    } else {
                        $rt['code'] = 0;
                        $rt['msg'] = '已经有置顶了';
                    }
                } else {
                    $rt['code'] = 0;
                    $rt['msg'] = '无此评论或不支持置顶';
                }
            } else {
                $rt['code'] = 0;
                $rt['msg'] = '未初始化';
            }
        } else {
            $rt['code'] = 0;
            $rt['msg'] = '你的档案中没有此评论框';
        }
    } else if ($act == 'deltop') {
        require p('users/' . $uid . '.php'); /*import userindex*/
        $frid = $_POST['id'];
        $akey = $_POST['a'];
        $mode = $_POST['md'];
        $myfrids = $myindex;
        if ((!empty($frid) || intval($frid) == 0) && array_key_exists(intval($frid), $myfrids)) {
            $frameid = $myfrids[$frid];
            if ($mode == 'single' && file_exists(p('frames/' . $frameid . '/' . $akey . '/index.php'))) {
                require p('frames/' . $frameid . '/' . $akey . '/index.php');
                $thetop = array();
                file_put_contents(p('frames/' . $frameid . '/' . $akey . '/index.php'), fileconst(array('comments' => $comments, 'thetop' => $thetop, 'commentsnum' => $commentsnum, 'orgakey' => $orgakey, 'floors' => $floors)));
            } else if ($mode == 'all' && file_exists(p('frames/' . $frameid . '/topindex.php'))) {
                require p('frames/' . $frameid . '/topindex.php');
                $tops = array();
                file_put_contents(p('frames/' . $frameid . '/topindex.php'), fileconst(array('tops' => $tops)));
            } else {
                $rt['code'] = 0;
                $rt['msg'] = '索引失败';
            }
        } else {
            $rt['code'] = 0;
            $rt['msg'] = '你的档案中没有此评论框';
        }
    }
} else {
    $rt['code'] = 0;
    $rt['msg'] = '未登录';
}
session_write_close();
echo json_encode($rt, JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES);
?>
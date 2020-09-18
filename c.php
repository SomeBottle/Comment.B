<?php
/*评论处理中枢*/
error_reporting(E_ALL ^ E_NOTICE ^ E_WARNING);
header('Content-type:text/json;charset=utf-8');
date_default_timezone_set("Asia/Shanghai");
$action = @$_GET['a'];
$rt = array('code' => 0);
/*Referer getter*/
$referer = $_SERVER['HTTP_REFERER'];
$rfhost = parse_url($referer, PHP_URL_HOST);
$rfscheme = parse_url($referer, PHP_URL_SCHEME);
$rfport = parse_url($referer, PHP_URL_PORT);
$rfport = !empty($rfport) ? ':' . $rfport : '';
header('Access-Control-Allow-Origin: ' . $rfscheme . '://' . $rfhost . $rfport);
header('Access-Control-Allow-Credentials: true');
/*因为前端请求附带withCredential才能在客户端储存cookie，但浏览器对此要求十分苛刻，故组装referer的部分以Access-Control-Allow-Origin头返回*/
function config($k) {
    require 'config.php';
    return $config[$k];
}
function p($p) {
    return './data/' . $p;
}
function md516($v) {
    return substr(md5($v), 8, 16);
}
function filter($c) { /*xss过滤*/
    return str_ireplace(array("\r\n", "\r", "\n"), ' <br> ', addslashes(htmlspecialchars($c, ENT_QUOTES))); /*这里换行标签<br>刻意留了空格，因为在前端识别url的时候可能会连同br一同牵连进去*/
}
function picg($str) { /*字串符图片url提取器*/
    $w = '/((https):\/\/)+(\S+\.)+(\S+)[\S\/\.\-]*(bmp|jpeg|jpg|gif|png|tif|pcx|svg|webp)/';
    preg_match_all($w, $str, $urls);
    return array('u' => $urls[0], 's' => trim(preg_replace($w, '', $str)));
}
function fileconst($arr) { /*储存文件组装器*/
    $re = '<?php ';
    foreach ($arr as $k => $v) {
        $re.= '$' . (is_array($v) ? ($k . '=' . var_export($v, true) . ';') : ($k . (is_numeric($v) ? ('=' . $v . ';') : ('=\'' . $v . '\';'))));
    }
    return $re . '?>';
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
/*Owner Auth*/
function ifown($frameid) {
    global $uid;
    if (isset($uid) && !empty($uid)) {
        require p('users/' . $uid . '.php');
        return in_array($frameid, $myindex, true) ? true : false;
    } else {
        return false;
    }
}
/*Renderer Initialization*/
function subrenderer($replies, $frameid, $akey, $mainrid) {
    global $uid;
    $ret = array();
    foreach ($replies as $k => $v) {
        require p('frames/' . $frameid . '/' . $akey . '/c' . $v['m'] . '.php');
        $replyid = intval($cm['rprid']);
        if ($replyid === $mainrid) $cm['rpnm'] = ''; /*如果回复的不是子评论，就删掉replyname*/
        unset($cm['owner']);
        $cm['date'] = date('Y年n月j日', $cm['unix']);
        $cm['content'] = base64_decode($cm['content']);
        $ret[$k] = array('m' => $cm);
    }
    return $ret;
}
function mainrenderer($floors, $frameid, $akey) {
    global $uid;
    $floorsj = array();
    foreach ($floors as $key => $val) {
        require p('frames/' . $frameid . '/' . $akey . '/c' . $val['m'] . '.php');
        unset($cm['owner']);
        $cm['date'] = date('Y年n月j日', $cm['unix']);
        $cm['content'] = base64_decode($cm['content']);
        $floorsj[$key] = array('m' => $cm, 'r' => array());
        $floorsj[$key]['rcut'] = false;
        if (!empty($val['r'])) { /*回复层*/
            $original_l = count($val['r']);
            $replies = array_slice($val['r'], 0, config('replylistmax')); /*只加载一部分*/
            $cut_l = count($replies);
            $mainrid = $val['m'];
            if ($original_l !== $cut_l) { /*被切割处理了*/
                $floorsj[$key]['rcut'] = true;
                $floorsj[$key]['rpart'] = config('replylistmax');
            }
            /*--------------subrenderer-------------------*/
            $floorsj[$key]['r'] = subrenderer($replies, $frameid, $akey, $mainrid);
        }
    }
    return $floorsj;
}
@session_start();
$usr = $_SESSION['commentuser']; /*get user details*/
$uid = $usr['id'];
if ($action == 'tp') {
    $template = file_get_contents('./assets/template.html');
    echo $template;
    session_write_close();
    exit();
} else if ($action == 'verify') {
    if (isset($usr) && isset($uid) && !empty($uid) && !file_exists(p('users/' . $uid . '.php'))) {
        file_put_contents(p('users/' . $uid . '.php'), fileconst(array('myindex' => array(), 'myframes' => array(), 'lastcount' => 0, 'commentsin' => 0)));
    }
    $dm = filter($_POST['dm']); /*get domain*/
    require p('frameindex.php'); /*import frameindex*/
    $frameid = '';
    foreach ($frames as $k => $v) { /*索引评论框*/
        if (in_array($rfhost, $v['domains'], true)) {
            $frameid = $k;
            break;
        }
    }
    if ($rfhost == $dm && !empty($frameid)) { /*判断host是否一致*/
        $rt['code'] = 1;
        $rt['frid'] = $frameid;
        $rt['ad'] = config('allowdelete');
        $rt['ifown'] = ifown($frameid);
    } else {
        $rt['code'] = 0;
    }
} else if ($action == 'gtcm') { /*Get comments*/
    $akey = md516($_POST['k']); /*防止特殊字符*/
    $frameid = filter($_POST['id']);
    if (is_dir(p('frames/' . $frameid))) {
        if (!file_exists(p('frames/' . $frameid . '/akeyindex.php'))) { /*创建akey对应储存区*/
            file_put_contents(p('frames/' . $frameid . '/akeyindex.php'), fileconst(array('akeys' => array())));
        }
        if (!file_exists(p('frames/' . $frameid . '/topindex.php'))) { /*创建评论框总置顶对应储存区*/
            file_put_contents(p('frames/' . $frameid . '/topindex.php'), fileconst(array('tops' => array())));
        }
        if (!is_dir(p('frames/' . $frameid . '/' . $akey))) { /*创建akey对应储存区*/
            mkdir(p('frames/' . $frameid . '/' . $akey));
        }
        if (!file_exists(p('frames/' . $frameid . '/' . $akey . '/index.php'))) { /*初始化楼层索引*/
            require p('frameindex.php');
            require p('frames/' . $frameid . '/akeyindex.php');
            $domains = $frames[$frameid]['domains'];
            if (in_array($rfhost, $domains, true)) { /*再检验referer*/
                file_put_contents(p('frames/' . $frameid . '/' . $akey . '/index.php'), fileconst(array('comments' => 0, 'thetop' => array(), 'commentsnum' => 0, 'orgakey' => base64_encode($_POST['k']), 'floors' => array())));
                $akeys[$akey] = base64_encode($_POST['k']);
                file_put_contents(p('frames/' . $frameid . '/akeyindex.php'), fileconst(array('akeys' => $akeys)));
            }
        }
        if (file_exists(p('frames/' . $frameid . '/' . $akey . '/index.php'))) {
            require p('frames/' . $frameid . '/' . $akey . '/index.php'); /*获取楼层索引*/
            require p('frames/' . $frameid . '/topindex.php');
            $rt['cmnum'] = $commentsnum;
            $rt['code'] = 1;
            $rt['cut'] = false;
            /*先组装置顶*/
            if (isset($thetop['c'])) { /*如果有局部置顶*/
                $idrc = commentindexer($thetop['c'], $floors) ['cid'];
                if ($idrc !== 'failed') $akeytop = array(0 => $floors[$idrc]);
                $atopdata = mainrenderer($akeytop, $frameid, $akey);
            }
            function getfloors($gk, $ak, $ori, $frameid) { /*获取全局评论对应的楼层*/
                if ($gk == $ak) { /*在当前页面*/
                    return array('r' => $ori, 'e' => true);
                } else {
                    require p('frames/' . $frameid . '/' . $gk . '/index.php'); /*获取楼层索引*/
                    return array('r' => $floors, 'e' => false);
                }
            }
            if (isset($tops['c'])) { /*如果有全局置顶*/
                $gkey = $tops['akey'];
                $gt = getfloors($gkey, $akey, $floors, $frameid);
                $gfloors = $gt['r'];
                $gea = $gt['e']; /*akey是否等于gkey，换而言之当前akey是不是全局置顶评论所属*/
                $idrc = commentindexer($tops['c'], $gfloors) ['cid'];
                if ($idrc !== 'failed') $globaltop = array(0 => $gfloors[$idrc]);
                $gtopdata = mainrenderer($globaltop, $frameid, $gkey);
            }
            /*开始组装楼层*/
            $original_l = count($floors);
            $floors = array_slice($floors, 0, config('commentlistmax')); /*只加载一部分*/
            $cut_l = count($floors);
            if ($original_l !== $cut_l) { /*被切割处理了*/
                $rt['cut'] = true;
                $rt['part'] = config('commentlistmax');
            }
            /*------------------main renderer----------------------*/
            $rt['data'] = mainrenderer($floors, $frameid, $akey);
            $rt['tops'] = array('a' => $atopdata, 'g' => $gtopdata, 'gea' => $gea);
        } else {
            $rt['code'] = 0;
        }
    } else {
        $rt['code'] = 0;
    }
} else if ($action == 'sm') { /*submit comment*/
    if (isset($usr)) {
        require p('users/' . $uid . '.php'); /*import userindex*/
        $limit = explode('per', config('commentcd'));
        $lnum = intval($limit[0]);
        $lduration = intval($limit[1]) * 60; /*convert to seconds*/
        $allowcomment = false;
        if ((time() - $lastcount) >= $lduration) {
            $commentsin = 0;
            $lastcount = time();
            $allowcomment = true; /*允许评论*/
        } else if ($commentsin >= ($lnum - 1)) {
            $rt['code'] = 0;
            $rt['msg'] = '评论发太多了，歇息一下吧';
        } else {
            $allowcomment = true;
        }
        if ($allowcomment) { /*通过评论验证*/
            $akey = md516($_POST['a']);
            $parsed = picg($_POST['c']);
            $pics = $parsed['u'];
            $content = filter($parsed['s']);
            $frameid = filter($_POST['id']);
            $ifreply = filter($_POST['rp']) !== 'false' ? true : false;
            $replyname = filter($_POST['rpnm']);
            $maxcontent = config('maxcontentlength');
            if (!empty($content) && file_exists(p('frames/' . $frameid . '/' . $akey . '/index.php'))) {
                if (strlen($content) <= $maxcontent) {
                    require p('frames/' . $frameid . '/' . $akey . '/index.php'); /*import floors index*/
                    $arr = array('rpnm' => $replyname, 'rid' => $comments, 'pics' => $pics, 'content' => base64_encode($content), 'unix' => time(), 'owner' => $uid, 'ua' => '', 'email' => md5($usr['email']), 'blog' => $usr['blog'], 'name' => $usr['name']);
                    $arr['rprid'] = $ifreply ? $_POST['rp'] : 0; /*记录回复的主评论id*/
                    $commentarr = $arr; /*储存评论文件内容*/
                    $commentsuccess = true; /*记录是否评论成功*/
                    if (!$ifreply) { /*非回复模式*/
                        $floorindex = array('m' => $comments, 'r' => array()); /*回复模块*/
                        array_unshift($floors, $floorindex); /*将新增评论加到顶部*/
                    } else { /*回复模式*/
                        $finds = commentindexer($_POST['rp'], $floors);
                        $cid = $finds['cid'];
                        $ifsub = $finds['sub'];
                        $subcid = $finds['subcid'];
                        $parentrid = $finds['parentrid'];
                        $floorindex = array('m' => $comments);
                        if ($cid !== 'failed' && !$ifsub) { /*非子评论回复*/
                            $arr['parentrid'] = $parentrid; /*提供主评论的RID*/
                            $arr['rpnm'] = ''; /*回复的主评论，就不现实replyname了*/
                            array_unshift($floors[$cid]['r'], $floorindex); /*将新增评论加到主评论下方顶部*/
                        } else if ($cid !== 'failed' && $ifsub) { /*子评论回复*/
                            $arr['parentrid'] = $parentrid; /*提供主评论的RID*/
                            array_splice($floors[$cid]['r'], $subcid + 1, 0, array(0 => $floorindex)); /*将新增评论加到子评论下方，这里有个arraysplice的奇怪问题，需要再套一个数组，太怪了*/
                        } else { /*找不到回复的评论*/
                            $commentsuccess = false;
                            $rt['msg'] = '找不到要回复的评论';
                        }
                    }
                    if ($commentsuccess) {
                        file_put_contents(p('frames/' . $frameid . '/' . $akey . '/c' . $comments . '.php'), fileconst(array('cm' => $commentarr)));
                        $comments+= 1; /*评论序列*/
                        $commentsnum+= 1; /*评论总数*/
                        $commentsin+= 1; /*用户在duration内评论的数量*/
                        file_put_contents(p('frames/' . $frameid . '/' . $akey . '/index.php'), fileconst(array('comments' => $comments, 'thetop' => $thetop, 'commentsnum' => $commentsnum, 'orgakey' => $orgakey, 'floors' => $floors)));
                        $arr['content'] = $content;
                        $arr['owner'] = true; /*是评论主，可以删除评论*/
                        unset($arr['unix']);
                        $arr['date'] = date('Y年n月j日', time());
                        $rt['data'] = $arr; /*返回到前端*/
                        $rt['reply'] = $ifreply ? true : false;
                    }
                    $rt['code'] = $commentsuccess ? 1 : 0;
                } else {
                    $rt['code'] = 0;
                    $rt['msg'] = '评论太长';
                }
            } else {
                $rt['code'] = 0;
                $rt['msg'] = '评论不能为空';
            }
        }
        file_put_contents(p('users/' . $uid . '.php'), fileconst(array('myindex' => $myindex, 'myframes' => $myframes, 'lastcount' => $lastcount, 'commentsin' => $commentsin)));
    } else {
        $rt['code'] = 0;
        $rt['msg'] = '未登录';
    }
} else if ($action == 'mr') { /*request for more*/
    $akey = md516($_POST['a']);
    $frameid = filter($_POST['id']);
    $mainrid = intval(filter($_POST['mrid']));
    $part = intval(filter($_POST['pt']));
    $ifsub = filter($_POST['sub']);
    $rt['code'] = 0;
    if (file_exists(p('frames/' . $frameid . '/' . $akey . '/index.php'))) {
        require p('frames/' . $frameid . '/' . $akey . '/index.php');
        $indexer = commentindexer($mainrid, $floors);
        $maincid = $indexer['cid'];
        $mainrid = $indexer['parentrid'];
        if ($ifsub == 'y') { /*申请的是子回复展开*/
            $data = array();
            $replies = $floors[$maincid]['r'];
            /*下方类似于上方gtcm内的方法*/
            $original_l = count($replies);
            $replies = array_slice($replies, $part, config('replylistmax')); /*再切一块出来*/
            $cutpart = $part + config('replylistmax');
            if ($original_l > $cutpart) { /*是否还能展开更多*/
                $data['rcut'] = true;
                $data['rpart'] = $cutpart;
            }
            $data['parentrid'] = $mainrid;
            /*--------------subrenderer-------------------*/
            $data['r'] = subrenderer($replies, $frameid, $akey, $mainrid);
            $rt['code'] = 1;
            $rt['data'] = $data;
        } else { /*申请的是主评论展开*/
            require p('frames/' . $frameid . '/' . $akey . '/index.php'); /*获取楼层索引*/
            $rt['cmnum'] = $commentsnum;
            $rt['code'] = 1;
            $rt['cut'] = false;
            /*开始组装楼层*/
            $original_l = count($floors);
            $floors = array_slice($floors, $part, config('commentlistmax')); /*只加载一部分*/
            $cutpart = $part + config('commentlistmax');
            if ($original_l > $cutpart) { /*是否还能展开更多*/
                $rt['cut'] = true;
                $rt['part'] = $cutpart;
            }
            /*------------------main renderer----------------------*/
            $rt['data'] = mainrenderer($floors, $frameid, $akey);
        }
    }
} else if ($action == 'dl') { /*delete comment*/
    $akey = md516($_POST['a']);
    $frameid = filter($_POST['id']);
    $rid = intval(filter($_POST['rid']));
    $own = ifown($frameid);
    $rt['code'] = 0;
    if (isset($usr)) {
        require p('frames/' . $frameid . '/' . $akey . '/index.php'); /*获取楼层索引*/
        require p('frames/' . $frameid . '/topindex.php'); /*获取总置顶索引*/
        $idr = commentindexer($rid, $floors);
        if ($idr['cid'] !== 'failed') { /*找到了*/
            require p('frames/' . $frameid . '/' . $akey . '/c' . $rid . '.php');
            if ($cm['owner'] == $uid || $own) {
                $cid = $idr['cid'];
                $subcid = $idr['subcid'];
                if ($idr['sub']) {
                    unset($floors[$cid]['r'][$subcid]);
                    $commentsnum-= 1;
                } else { /*删除的是主评论*/
                    $subs = $floors[$cid]['r'];
                    foreach ($subs as $v) {
                        @unlink(p('frames/' . $frameid . '/' . $akey . '/c' . $v['m'] . '.php')); /*删除楼层下子评论储存文件*/
                    }
                    unset($floors[$cid]); /*删除评论索引*/
                    $commentsnum-= (count($subs) + 1); /*把楼层的子评论全算进去*/
                    /*如果有置顶也一起删了，全局置顶rid相同的同时还要保证akey相同，不然会误删*/
                    if ($tops['c'] === $rid && $tops['akey'] == $akey) {
                        $tops = array();
                        file_put_contents(p('frames/' . $frameid . '/topindex.php'), fileconst(array('tops' => $tops)));
                    } else if ($thetop['c'] === $rid) {
                        $thetop = array();
                    }
                }
                @unlink(p('frames/' . $frameid . '/' . $akey . '/c' . $rid . '.php')); /*删除评论储存文件*/
                $rt['code'] = 1;
                file_put_contents(p('frames/' . $frameid . '/' . $akey . '/index.php'), fileconst(array('comments' => $comments, 'thetop' => $thetop, 'commentsnum' => $commentsnum, 'orgakey' => $orgakey, 'floors' => $floors)));
            } else {
                $rt['msg'] = '你没有权限删除此评论';
            }
        } else {
            $rt['msg'] = '无此评论';
        }
    } else {
        $rt['msg'] = '未登录';
    }
}
session_write_close();
echo json_encode($rt, JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES);
?>